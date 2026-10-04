package coord

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// holdRecord is one hold in force, as holdsFold folds it.
type holdRecord struct {
	ID     string
	Key    string
	Scope  holdScope
	Kind   string
	Engine engine.Name
	Source engine.CredentialSource
	By     string
	Since  time.Time
	// Until is the self-release deadline; zero is none.
	Until time.Time
	// Fingerprint is the refused credential's digest (holdOpened).
	Fingerprint string
	// Members are the harps the hold covers, each with the run it parked — ""
	// once that run ended. A harp stays held after its run ends: its
	// relaunch waits for the release (launchgate.go, relaunchForLeftoverMail).
	Members map[string]string
}

// pause reports whether the hold is an initiator's pause rather than a turn
// failure's backoff.
func (h *holdRecord) pause() bool { return h.Kind == HoldKindHuman || h.Kind == HoldKindAgent }

// owedResume is a run whose hold was released before its runner acked the
// resume.
type owedResume struct{ key, harp string }

// holdsFold is every hold in force and every resume still owed, folded from
// the hold facts, run.launched and run.ended over the run-registry journal —
// the SAME ordered stream as the runs, so "a run that ended is in no hold"
// is a fold rule, deterministic under replay. It owns no timers.
type holdsFold struct {
	byKey    map[string]*holdRecord
	byRun    map[string]string // live member run id → its hold's key
	byHarp   map[string]string // member harp → its hold's key
	owed     map[string]owedResume
	launched map[string]runLaunched
}

func newHoldsFold() *holdsFold {
	return &holdsFold{
		byKey:    make(map[string]*holdRecord),
		byRun:    make(map[string]string),
		byHarp:   make(map[string]string),
		owed:     make(map[string]owedResume),
		launched: make(map[string]runLaunched),
	}
}

func (f *holdsFold) apply(fact Fact) {
	switch fact.Kind {
	case factHoldOpened:
		applyDecoded(fact, f.applyOpened)
	case factHoldParked:
		applyDecoded(fact, f.applyParked)
	case factHoldExtended:
		applyDecoded(fact, f.applyExtended)
	case factHoldDropped:
		applyDecoded(fact, f.applyDropped)
	case factHoldReleased:
		applyDecoded(fact, f.applyReleased)
	case factHoldResumed:
		applyDecoded(fact, f.applyResumed)
	case factRunLaunched:
		applyDecoded(fact, func(p runLaunched, _ time.Time) { f.launched[p.RunID] = p })
	case factRunEnded:
		applyDecoded(fact, f.applyEnded)
	}
}

func (f *holdsFold) applyOpened(p holdOpened, at time.Time) {
	if _, inForce := f.byKey[p.Key]; inForce {
		return // one hold per key; decide never opens a second
	}
	f.byKey[p.Key] = &holdRecord{
		ID: p.ID, Key: p.Key, Scope: p.Scope, Kind: p.Kind, Engine: p.Engine, Source: p.Source,
		By: p.By, Since: at, Until: p.Until, Fingerprint: p.Fingerprint, Members: make(map[string]string),
	}
}

func (f *holdsFold) applyParked(p holdParked, _ time.Time) {
	h := f.byKey[p.Key]
	if h == nil {
		return
	}
	h.Members[p.Harp] = p.RunID
	f.byRun[p.RunID] = p.Key
	f.byHarp[p.Harp] = p.Key
}

func (f *holdsFold) applyExtended(p holdExtended, _ time.Time) {
	if h := f.byKey[p.Key]; h != nil {
		h.Kind, h.Until = p.Kind, p.Until
	}
}

func (f *holdsFold) applyDropped(p holdDropped, _ time.Time) {
	h := f.byKey[p.Key]
	if h == nil {
		return
	}
	delete(h.Members, p.Harp)
	delete(f.byRun, p.RunID)
	delete(f.byHarp, p.Harp)
}

func (f *holdsFold) applyReleased(p holdReleased, _ time.Time) {
	h := f.byKey[p.Key]
	if h == nil {
		return
	}
	for harp, runID := range h.Members {
		if runID != "" {
			f.owed[runID] = owedResume{key: p.Key, harp: harp}
			delete(f.byRun, runID)
		}
		delete(f.byHarp, harp)
	}
	delete(f.byKey, p.Key)
}

func (f *holdsFold) applyResumed(p holdResumed, _ time.Time) { delete(f.owed, p.RunID) }

// applyEnded: a dead run is in no hold and owed nothing, but its harp stays
// covered — the hold, not the run's end, decides when the harp may run again.
func (f *holdsFold) applyEnded(p runEnded, _ time.Time) {
	if key, held := f.byRun[p.RunID]; held {
		h := f.byKey[key]
		for harp, runID := range h.Members {
			if runID == p.RunID {
				h.Members[harp] = ""
			}
		}
		delete(f.byRun, p.RunID)
	}
	delete(f.owed, p.RunID)
	delete(f.launched, p.RunID)
}

// inForce is every hold in force, oldest first, as copies.
func (f *holdsFold) inForce() []holdRecord {
	out := make([]holdRecord, 0, len(f.byKey))
	for _, h := range f.byKey {
		cp := *h
		cp.Members = maps.Clone(h.Members)
		out = append(out, cp)
	}
	slices.SortFunc(out, func(a, b holdRecord) int { return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.Key, b.Key)) })
	return out
}

// holdOfRun is the hold parking runID, nil when none does. Read it inside
// Store.View/Exec only.
func (f *holdsFold) holdOfRun(runID string) *holdRecord { return f.byKey[f.byRun[runID]] }

// holdOfHarp is the hold covering harp, nil when none does.
func (f *holdsFold) holdOfHarp(harp string) *holdRecord { return f.byKey[f.byHarp[harp]] }

// owedOf is the key of the released hold whose resume runID is still owed.
func (f *holdsFold) owedOf(runID string) (key string, ok bool) {
	o, ok := f.owed[runID]
	return o.key, ok
}

// launchOf is runID's journaled launch identity.
func (f *holdsFold) launchOf(runID string) (runLaunched, bool) {
	l, ok := f.launched[runID]
	return l, ok
}
