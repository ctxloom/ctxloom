package coord

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// A CREDENTIAL HOLD parks every run that shares a credential whose engine
// turned a turn away on a held failure (heldFailures). Without it each child
// meets the spent limit on its own next turn, consumes that turn's mail for
// nothing, and an unattended run thrashes one child at a time.
//
// The run whose turn was turned away has already parked itself (its runner
// raised the pause gate before reporting idle); the hold pauses the others
// with the same source (launch.Cell.Credential) and raises ONE finding. One
// hold per credential, with two ways out:
//   - its deadline (Until): a limit lifts on its own, so the hold releases
//     itself, resuming every run it parked;
//   - the HUMAN's resume of any held harp releases all of them early.
//
// A limit still spent fails the next turn and parks again. Holds are this
// coordinator's, shared by its children only.
//
// An OVERLOADED turn (the engine's server at capacity) takes the same
// machinery with the run's own key (holdKey): capacity is not the
// credential's quota, so parking the runs sharing it would idle healthy
// siblings. The run alone backs off for overloadBackoff, with the same two
// ways out.
//
// An initiator's PAUSE (ControlPause) is a hold too, of kind human or agent,
// with no deadline: whoever may control the run releases it.
//
// A hold covers HARPS, not just runs: a member whose run ends (its runner
// lost, say) stays held, and nothing relaunches it — not its leftover mail,
// not a send — until the hold releases, which then relaunches it if mail is
// waiting.
//
// DURABILITY: the run journal's hold facts (facts.go) are the only record of
// a hold; holdsFold folds them. Every change is journaled BEFORE it is acted
// on (a pause sent, a resume sent), so a coordinator that dies at any point
// re-asserts what its journal says on adopt: holds still in force are re-armed
// (or released, if their deadline passed while it was down), and once each
// run's runner re-Hellos, a held run's pause and a released run's owed resume
// are sent again. Both are idempotent at the runner.

// holdScope is what a hold pauses: every run sharing a credential, or one run.
type holdScope string

const (
	holdScopeCredential holdScope = "credential"
	holdScopeRun        holdScope = "run"
)

// The kinds of a pause hold, by who paused (ControlInitiator.Kind), as
// RunHold.Kind spells them. A failure's hold carries its agent.FailureKind.
const (
	HoldKindHuman = "human"
	HoldKindAgent = "agent"
)

// heldFailures are the turn failures a run parks itself on and its
// credential's hold releases. The runner parks only on these (HoldsFailure):
// a self-park on any other kind would be a pause nothing releases.
var heldFailures = []agent.FailureKind{agent.FailureRateLimited, agent.FailureOverloaded}

// holdFailures are the failures a hold can be journaled for: the held
// failures, and a refused credential, whose hold has no deadline.
var holdFailures = append(slices.Clone(heldFailures), agent.FailureCredentialRejected)

// failureKindOf is the failure a hold's journaled kind names; false for a
// pause's kind, or one this build does not know.
func failureKindOf(kind string) (agent.FailureKind, bool) {
	i := slices.IndexFunc(holdFailures, func(k agent.FailureKind) bool { return string(k) == kind })
	if i < 0 {
		return "", false
	}
	return holdFailures[i], true
}

// HoldsFailure reports whether a turn that failed with kind parks its run
// until its credential's hold releases it.
func HoldsFailure(kind agent.FailureKind) bool {
	return slices.Contains(heldFailures, kind)
}

// The backoff policy for a hold: the engine's reset time, clamped. No jitter:
// one timer per credential releases its runs TOGETHER, which is the point; if
// the released turns meet the limit again, the first to fail parks the rest.
// Hard-coded: a tunable would be a config contract nothing yet shows a need
// for.
const (
	// rateLimitFloor: the engine already retried before its turn gave up (claude
	// retries temporary 429s itself), so a reset time in the past, or the next
	// instant, must not resume runs straight back into the limit.
	rateLimitFloor = 30 * time.Second
	// rateLimitDefault: the wait when the engine named no reset time — one
	// probe per credential per period, not one per child.
	rateLimitDefault = 5 * time.Minute
	// rateLimitCap: a bogus or distant reset (a weekly window) must not hold
	// runs for days with nothing changing; at the cap the hold releases, and a
	// limit still spent fails the next turn, which re-measures it.
	rateLimitCap = 6 * time.Hour
	// overloadBackoff: the wait after an overloaded turn. The engine already
	// retried the overload with its own backoff before the turn gave up
	// (claude retries 529s itself), and a 529 names no recovery time: one
	// probe per run per period, re-measured by the next turn.
	overloadBackoff = time.Minute
)

// holdDeadline is when a hold for failure f, folded at now, releases itself.
func holdDeadline(now time.Time, f agent.TurnFailure) time.Time {
	if f.Kind == agent.FailureOverloaded {
		return now.Add(overloadBackoff)
	}
	return backoffDeadline(now, f.ResetsAt)
}

// backoffDeadline is when a hold on a limit the engine says resets at resetsAt
// (zero: it did not say) releases itself.
func backoffDeadline(now, resetsAt time.Time) time.Time {
	switch {
	case resetsAt.IsZero():
		return now.Add(rateLimitDefault)
	case resetsAt.Before(now.Add(rateLimitFloor)):
		return now.Add(rateLimitFloor)
	case resetsAt.After(now.Add(rateLimitCap)):
		return now.Add(rateLimitCap)
	}
	return resetsAt
}

// CredentialHold is one held credential and the harps it covers.
type CredentialHold struct {
	Engine engine.Name
	Source engine.CredentialSource
	Kind   agent.FailureKind
	Since  time.Time
	// Until is when the hold releases itself; zero for a hold with no
	// deadline, which only the human releases.
	Until time.Time
	// Harps are the harps this hold covers, sorted.
	Harps []string
}

// ErrCredentialHeld refuses an agent's resume of a run parked on a hold (its
// credential's limit, or its own overload backoff): an agent that resumed it
// early would only meet the same refusal, and re-raise the human's notice.
var ErrCredentialHeld = errors.New("coord: the run is parked on a hold (its credential's rate limit, or its own overload backoff); only the human, or the hold's own backoff, releases it")

// ErrHumanPaused refuses an agent's resume of a run the HUMAN paused: the
// human opened that pause, and only the human ends it.
var ErrHumanPaused = errors.New("coord: the human paused this run; only the human may resume it")

// holdLocal is what a hold needs that no journal can carry: its armed timer,
// and the in-process park barrier. Guarded by Coordinator.holdMu.
type holdLocal struct {
	id string // the hold (holdOpened.ID) it belongs to
	// stop disarms the timer, and gen names the one armed timer whose firing
	// still counts — a Stop can lose to a callback already on its way.
	stop func() bool
	gen  int
	// parked is closed once parkSiblings has sent every pause it will send. A
	// hold is never released before: a pause landing after the release's
	// resume would leave that run paused in no hold, forever.
	parked chan struct{}
}

// Steps a test can hold at (Coordinator.holdStep).
const (
	holdStepParkSibling = "park-sibling" // before a sibling's pause is sent
	holdStepReleaseWait = "release-wait" // a release, before it waits for the hold's parking
	// holdStepTurnFolded: a turn's failure, once its hold has taken it in. A
	// failure that JOINS a hold changes nothing observable, so this is the
	// only sign it landed.
	holdStepTurnFolded = "turn-folded"
	holdStepResume     = "resume" // a released hold's resume, before it is sent
	// holdStepReasserted: a re-adopted run's journaled hold (its pause, or its
	// owed resume) has been re-sent to its runner, or could not be.
	holdStepReasserted = "reasserted"
	// holdStepRelaunchHeld: a held harp's relaunch was deferred to its release.
	holdStepRelaunchHeld = "relaunch-held"
)

// step reports a hold step to the test seam, when one is set.
func (c *Coordinator) step(name string) {
	if c.holdStep != nil {
		c.holdStep(name)
	}
}

// CredentialHolds is every turn failure's hold in force, oldest first. Pauses
// are not credential holds.
func (c *Coordinator) CredentialHolds() []CredentialHold {
	var out []CredentialHold
	c.runs.View(func() {
		for _, h := range c.holdsF.inForce() {
			kind, failure := failureKindOf(h.Kind)
			if !failure {
				continue
			}
			out = append(out, CredentialHold{
				Engine: h.Engine, Source: h.Source, Kind: kind, Since: h.Since, Until: h.Until,
				Harps: slices.Sorted(maps.Keys(h.Members)),
			})
		}
	})
	return out
}

// runHolds is the roster's view of every run a hold parks — a failure's or a
// pause — by run id. Its Source is the credential's carrier names, not the
// hold's key: a run's own hold (an overload, or a run with no credential) is
// keyed by its run id, which names no carrier, and a pause has none.
func (c *Coordinator) runHolds() map[string]*RunHold {
	out := make(map[string]*RunHold)
	c.runs.View(func() {
		for runID := range c.holdsF.byRun {
			h := c.holdsF.holdOfRun(runID)
			out[runID] = &RunHold{Kind: h.Kind, Source: h.Source.Key, Until: h.Until}
		}
	})
	return out
}

// pausedRunIDs is every run held at its runner's gate by this coordinator: a
// hold parks it, or a released hold still owes it its resume.
func (c *Coordinator) pausedRunIDs() map[string]bool {
	out := make(map[string]bool)
	c.runs.View(func() {
		for id := range c.holdsF.byRun {
			out[id] = true
		}
		for id := range c.holdsF.owed {
			out[id] = true
		}
	})
	return out
}

// runPaused reports whether runID is held at its runner's gate by this
// coordinator (pausedRunIDs).
func (c *Coordinator) runPaused(runID string) bool { return c.pausedRunIDs()[runID] }

// harpHeld reports whether a hold covers harp, whether or not its run lives.
func (c *Coordinator) harpHeld(harp string) bool {
	held := false
	c.runs.View(func() { held = c.holdsF.holdOfHarp(harp) != nil })
	return held
}

// holdScopeOf is the scope of a hold for a turn that failed with kind on
// src: the credential's, or the run's own — for an overload (the server's
// capacity, not the credential's) and for a run that carries no credential.
func holdScopeOf(kind agent.FailureKind, src engine.CredentialSource) holdScope {
	if kind != agent.FailureOverloaded && src.Key != "" {
		return holdScopeCredential
	}
	return holdScopeRun
}

// holdKey is the hold a run that failed with kind belongs to (holdScopeOf):
// its source's key, or a key of the run's own.
func holdKey(kind agent.FailureKind, src engine.CredentialSource, runID string) string {
	if holdScopeOf(kind, src) == holdScopeCredential {
		return src.Key
	}
	return "run:" + runID
}

// pauseKey is the key of an initiator's pause of harp.
func pauseKey(harp string) string { return "pause:" + harp }

// recordLaunch journals a run's resolved engine and credential source: the
// identity that keys it into a credential's hold (holdsFold.launchOf), for a
// run this process started and for one it re-adopted after a restart alike.
func (c *Coordinator) recordLaunch(runID string, l launch.Launch) {
	at := c.now()
	if err := c.runs.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factRunLaunched, at, runLaunched{RunID: runID, Engine: l.Engine, Source: l.Cell.Credential})}, nil
	}); err != nil {
		c.rep.Warnf("coordinator: could not journal %s's launch; a restart will not know its credential: %v", runID, err)
	}
}

// heldRun is one run a hold parks.
type heldRun struct{ runID, harp string }

// turnFailureOf is the held failure a turn-idle event's value names (nil for a
// turn that ended otherwise). A reset time that does not parse is no reset
// time: the policy's default bounds the wait.
func turnFailureOf(value map[string]any) *agent.TurnFailure {
	stop, _ := value["stop_reason"].(string)
	i := slices.IndexFunc(heldFailures, func(k agent.FailureKind) bool { return string(k) == stop })
	if i < 0 {
		return nil
	}
	f := &agent.TurnFailure{Kind: heldFailures[i]}
	if at, ok := value[TurnIdleResetsAt].(string); ok {
		f.ResetsAt, _ = time.Parse(time.RFC3339, at)
	}
	return f
}

// turnFold is what folding one turned-away turn into its hold decided.
type turnFold struct {
	opened, moved bool
	parkedNothing bool // a new hold had nothing to park, so none was opened
	until         time.Time
	siblings      []heldRun
}

// failedTurn is one turned-away turn, with what its hold is decided from.
type failedTurn struct {
	f          agent.TurnFailure
	own        heldRun
	eng        engine.Name
	src        engine.CredentialSource
	key        string
	scope      holdScope
	candidates []heldRun // every other attached run with the same key
}

// onTurnFailed folds a run's turned-away turn into its hold (holdKey): the
// run joins it (opening it if none is in force), and a new hold parks every
// other run with the same key. A run under an initiator's PAUSE stays the
// pauser's — left out of the hold, so the release never undoes that pause —
// but its failure still parks its siblings: the limit is as spent for them.
// A run already parked by ANOTHER failure's hold (a sibling's limit reached
// it mid-turn, and that turn then ended overloaded) stays in that hold: its
// release resumes the run, and a second hold would resume it early.
func (c *Coordinator) onTurnFailed(role, runID string, f agent.TurnFailure) {
	defer c.step(holdStepTurnFolded)
	t, ok := c.failedTurnOf(role, runID, f)
	if !ok {
		return
	}
	d, local, err := c.foldTurnFailure(t)
	switch {
	case err != nil:
		c.rep.Warnf("coordinator: could not journal %s's turn failure into its hold: %v", t.own.harp, err)
	case d.opened:
		c.raiseHoldFinding(t.eng, f.Kind, t.key, t.own.harp, d.until, true)
		c.goTracked(func() { c.parkSiblings(t.key, local, d.siblings) })
	case d.parkedNothing:
		c.raiseHoldFinding(t.eng, f.Kind, t.key, t.own.harp, time.Time{}, false)
	}
}

// failedTurnOf reads what runID's turned-away turn is decided from: the
// attached runs (under c.mu), keyed by their journaled launches. False for a
// run no longer attached.
func (c *Coordinator) failedTurnOf(role, runID string, f agent.TurnFailure) (failedTurn, bool) {
	c.mu.Lock()
	rt := c.runtimeForLocked(role, runID)
	if rt == nil {
		c.mu.Unlock()
		return failedTurn{}, false
	}
	t := failedTurn{f: f, own: heldRun{runID, rt.harp}}
	attached := make([]heldRun, 0, len(c.attach))
	for id, srt := range c.attach {
		if id != runID {
			attached = append(attached, heldRun{id, srt.harp})
		}
	}
	c.mu.Unlock()
	c.runs.View(func() {
		l, _ := c.holdsF.launchOf(runID)
		t.eng, t.src = l.Engine, l.Source
		t.key, t.scope = holdKey(f.Kind, t.src, runID), holdScopeOf(f.Kind, t.src)
		for _, r := range attached {
			if s, _ := c.holdsF.launchOf(r.runID); holdKey(f.Kind, s.Source, r.runID) == t.key {
				t.candidates = append(t.candidates, r)
			}
		}
	})
	slices.SortFunc(t.candidates, func(a, b heldRun) int { return cmp.Compare(a.runID, b.runID) })
	return t, true
}

// foldTurnFailure journals t into its hold under c.holdMu, keeping the hold's
// timer in step. A hold that may open here gets its timer armed BEFORE it is
// journaled, so no reader ever sees a hold in force with no timer to release
// it: which holds are in force changes only under holdMu, so whether this one
// opens is already settled, and one that opens nothing (decide parks no one)
// is disarmed again. local is the opened hold's.
func (c *Coordinator) foldTurnFailure(t failedTurn) (d turnFold, local *holdLocal, err error) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	now := c.now()
	c.runs.View(func() {
		if c.holdsF.byKey[t.key] == nil {
			local = &holdLocal{id: RandID("hold-", 12), parked: make(chan struct{})}
		}
	})
	if local != nil {
		c.holds[t.key] = local
		c.armLocked(t.key, local, holdDeadline(now, t.f))
	}
	err = c.runs.Exec(func() ([]Fact, error) {
		var facts []Fact
		d, facts = c.decideTurnFailure(t, now, local)
		return facts, nil
	})
	switch {
	case local != nil && (err != nil || !d.opened):
		c.armLocked(t.key, local, time.Time{})
		delete(c.holds, t.key)
		local = nil
	case err == nil && d.moved:
		if l := c.holds[t.key]; l != nil {
			c.armLocked(t.key, l, d.until)
		}
	}
	return d, local, err
}

// decideTurnFailure is foldTurnFailure's decision, inside the run journal's
// Exec: it reads only the folds. local is the hold armed for a key with none
// in force (nil when one is).
func (c *Coordinator) decideTurnFailure(t failedTurn, now time.Time, local *holdLocal) (turnFold, []Fact) {
	if r := c.runsF.run(t.own.runID); r == nil || r.Ended {
		return turnFold{}, nil // a turn boundary after the run's terminal folds nothing
	}
	other := c.holdsF.holdOfRun(t.own.runID)
	if other != nil && other.Key != t.key && !other.pause() {
		return turnFold{}, nil
	}
	ownPark := other == nil
	if rec := c.holdsF.byKey[t.key]; rec != nil {
		return c.joinHold(t, now, rec, ownPark)
	}
	if local == nil {
		return turnFold{}, nil // unreachable: holds come into force only under holdMu
	}
	return c.openHold(t, now, local.id, ownPark)
}

// openHold opens t's hold, parking its own run (unless paused) and every
// live, unheld sibling — or decides there is nothing to park.
func (c *Coordinator) openHold(t failedTurn, now time.Time, id string, ownPark bool) (turnFold, []Fact) {
	var d turnFold
	for _, s := range t.candidates {
		if r := c.runsF.run(s.runID); r != nil && !r.Ended && c.holdsF.holdOfRun(s.runID) == nil {
			d.siblings = append(d.siblings, s)
		}
	}
	if !ownPark && len(d.siblings) == 0 {
		d.parkedNothing = true
		return d, nil
	}
	d.opened, d.until = true, holdDeadline(now, t.f)
	facts := []Fact{factAt(factHoldOpened, now, holdOpened{ID: id, Key: t.key, Scope: t.scope, Kind: string(t.f.Kind), Engine: t.eng, Source: t.src, Until: d.until})}
	if ownPark {
		facts = append(facts, factAt(factHoldParked, now, holdParked{Key: t.key, RunID: t.own.runID, Harp: t.own.harp, Cause: "turn"}))
	}
	for _, s := range d.siblings {
		facts = append(facts, factAt(factHoldParked, now, holdParked{Key: t.key, RunID: s.runID, Harp: s.harp, Cause: "sibling"}))
	}
	return d, facts
}

// joinHold folds t into rec, the hold in force under its key. A deadline only
// ever moves out — an earlier reset never shortens a wait another run's limit
// set — and a hold with NO deadline is not given one, or relabelled, by a
// later failure.
func (c *Coordinator) joinHold(t failedTurn, now time.Time, rec *holdRecord, ownPark bool) (turnFold, []Fact) {
	var d turnFold
	var facts []Fact
	if !rec.Until.IsZero() {
		d.until = rec.Until
		if dl := holdDeadline(now, t.f); dl.After(rec.Until) {
			d.until, d.moved = dl, true
		}
		if d.moved || rec.Kind != string(t.f.Kind) {
			facts = append(facts, factAt(factHoldExtended, now, holdExtended{Key: t.key, Kind: string(t.f.Kind), Until: d.until}))
		}
	}
	if ownPark {
		facts = append(facts, factAt(factHoldParked, now, holdParked{Key: t.key, RunID: t.own.runID, Harp: t.own.harp, Cause: "turn"}))
	}
	return d, facts
}

// armLocked (re)arms local's timer for until, or disarms it for none (zero).
// Caller holds c.holdMu.
func (c *Coordinator) armLocked(key string, local *holdLocal, until time.Time) {
	if local.stop != nil {
		local.stop()
		local.stop = nil
	}
	local.gen++
	if until.IsZero() {
		return
	}
	gen := local.gen
	local.stop = c.afterFunc(until.Sub(c.now()), func() { c.backoffElapsed(key, local, gen) })
}

// backoffElapsed is a hold's own release: it counts only for the hold still in
// force under key, armed as gen.
func (c *Coordinator) backoffElapsed(key string, local *holdLocal, gen int) {
	still := func() bool { return c.holds[key] == local && local.gen == gen }
	c.holdMu.Lock()
	ok := still()
	c.holdMu.Unlock()
	if !ok {
		return
	}
	c.step(holdStepReleaseWait)
	<-local.parked
	members, released := c.releaseKey(key, local.id, "backoff", "", still)
	if !released {
		return
	}
	ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
	defer cancel()
	c.settleRelease(ctx, key, members)
}

// releaseKey journals the release of the hold in force under key — if it is
// still the hold id and still (under c.holdMu) wanted — and disarms its timer.
// members are the harps it covered, each with its live run ("" for one that
// ended); every live run is now owed a resume (settleRelease).
func (c *Coordinator) releaseKey(key, id, cause, by string, still func() bool) (members map[string]string, released bool) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	if !still() {
		return nil, false
	}
	at := c.now()
	err := c.runs.Exec(func() ([]Fact, error) {
		h := c.holdsF.byKey[key]
		if h == nil || h.ID != id {
			return nil, nil
		}
		members, released = maps.Clone(h.Members), true
		return []Fact{factAt(factHoldReleased, at, holdReleased{Key: key, Cause: cause, By: by})}, nil
	})
	if err != nil {
		c.rep.Warnf("coordinator: could not journal the release of hold %s: %v", key, err)
		return nil, false
	}
	if l := c.holds[key]; released && l != nil && l.id == id {
		c.armLocked(key, l, time.Time{})
		delete(c.holds, key)
	}
	return members, released
}

// always is releaseKey's still for a release nothing can supersede.
func always() bool { return true }

// resumeOutcome is one owed resume's delivery: the runner's own word on
// whether it lifted its gate, or why it could not be reached.
type resumeOutcome struct {
	newly bool
	err   error
}

// settleRelease acts on a journaled release: each member run still live gets
// its owed resume, and each member harp whose run ended is relaunched if mail
// waits for it.
func (c *Coordinator) settleRelease(ctx context.Context, key string, members map[string]string) map[string]resumeOutcome {
	out := make(map[string]resumeOutcome, len(members))
	for _, harp := range slices.Sorted(maps.Keys(members)) {
		runID := members[harp]
		if runID == "" {
			c.relaunchReleased(harp)
			continue
		}
		newly, err := c.deliverOwedResume(ctx, key, heldRun{runID, harp})
		out[runID] = resumeOutcome{newly, err}
	}
	return out
}

// deliverOwedResume sends r's owed resume and, once its runner acks, journals
// that nothing is owed. A resume that cannot be sent stays owed: a restart
// re-sends it once the runner is back (readoptHold).
func (c *Coordinator) deliverOwedResume(ctx context.Context, key string, r heldRun) (bool, error) {
	c.step(holdStepResume)
	resp, err := c.holdControl(ctx, r, "resume", "")
	if err != nil {
		c.rep.Warnf("coordinator: could not resume %s from its hold: %v", r.harp, err)
		return false, err
	}
	c.ackOwed(key, r)
	res, _ := resp.Kind.(ResumeRunResult)
	return res.NewlyResumed, nil
}

// ackOwed journals that r's resume, owed by the hold under key, landed.
func (c *Coordinator) ackOwed(key string, r heldRun) {
	at := c.now()
	if err := c.runs.Exec(func() ([]Fact, error) {
		if owedKey, ok := c.holdsF.owedOf(r.runID); !ok || owedKey != key {
			return nil, nil
		}
		return []Fact{factAt(factHoldResumed, at, holdResumed{Key: key, RunID: r.runID, Harp: r.harp})}, nil
	}); err != nil {
		c.rep.Warnf("coordinator: could not journal %s's resume: %v", r.harp, err)
	}
}

// raiseHoldFinding tells the root human what the hold is and when it ends.
// parked is false for a failure that parked nothing (the run's own pause held
// it, and no other run shares its key): no hold is in force. An overload that
// parked nothing has nothing to tell: no other run was ever at stake.
func (c *Coordinator) raiseHoldFinding(eng engine.Name, kind agent.FailureKind, key, harp string, until time.Time, parked bool) {
	who := cmp.Or(string(eng), "the engine")
	if kind == agent.FailureOverloaded {
		if parked {
			c.rep.Warnf("coordinator: %s was overloaded on %s's turn; that run alone backs off until %s and resumes on its own",
				who, harp, until.UTC().Format(time.RFC3339))
		}
		return
	}
	if !parked {
		c.rep.Warnf("coordinator: %s's credential hit its rate limit (%s); no other run shares it, so nothing else is parked", who, key)
		return
	}
	c.rep.Warnf("coordinator: %s's credential hit its rate limit (%s); the runs sharing it are parked until %s and resume on their own",
		who, key, until.UTC().Format(time.RFC3339))
}

// parkSiblings pauses each sibling the journal parked, then marks the hold's
// parking done. One that cannot be paused (ended, or its runner gone) leaves
// the hold.
func (c *Coordinator) parkSiblings(key string, local *holdLocal, siblings []heldRun) {
	defer close(local.parked)
	for _, r := range siblings {
		c.step(holdStepParkSibling)
		ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
		_, err := c.holdControl(ctx, r, "pause", "the credential it shares hit its rate limit")
		cancel()
		if err != nil {
			c.dropFromHold(key, local.id, r)
			c.rep.Warnf("coordinator: could not park %s on its credential's hold: %v", r.harp, err)
		}
	}
}

// holdControl sends a hold's pause or resume to r's runner, as the
// coordinator itself: no initiator's ownership applies.
func (c *Coordinator) holdControl(ctx context.Context, r heldRun, verb, reason string) (RunnerResponse, error) {
	rec := c.currentRunRecord(r.harp)
	if rec == nil || rec.RunID != r.runID || rec.Ended {
		return RunnerResponse{}, fmt.Errorf("run %s of %q is no longer current", r.runID, r.harp)
	}
	return c.sendRunnerControl(ctx, rec, verb, reason)
}

// dropFromHold takes r's harp out of the hold id under key — and the hold out
// of force if that empties it, which a hold still parking cannot be released
// by anything else.
func (c *Coordinator) dropFromHold(key, id string, r heldRun) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	c.dropHarpLocked(r.harp, func(h *holdRecord) bool { return h.Key == key && h.ID == id && h.Members[r.harp] == r.runID })
}

// dropStoppedHarp takes a harp an agent_stop ended out of whatever hold or
// pause covers it: the stop is an initiator's judgement on the child, so its
// next run is not the hold's to keep waiting. A credential's hold still parks
// that next run as it comes up (joinCredentialHold): it is the credential's.
func (c *Coordinator) dropStoppedHarp(harp string) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	c.dropHarpLocked(harp, func(*holdRecord) bool { return true })
}

// dropHarpLocked journals harp leaving the hold covering it, when match
// accepts that hold — releasing the hold (cause "empty") if harp was its last
// member, and disarming its timer. Caller holds c.holdMu.
func (c *Coordinator) dropHarpLocked(harp string, match func(*holdRecord) bool) {
	at := c.now()
	var emptied *holdRecord
	err := c.runs.Exec(func() ([]Fact, error) {
		h := c.holdsF.holdOfHarp(harp)
		if h == nil || !match(h) {
			return nil, nil
		}
		facts := []Fact{factAt(factHoldDropped, at, holdDropped{Key: h.Key, RunID: h.Members[harp], Harp: harp})}
		if len(h.Members) == 1 {
			emptied = h
			facts = append(facts, factAt(factHoldReleased, at, holdReleased{Key: h.Key, Cause: "empty"}))
		}
		return facts, nil
	})
	if err != nil {
		c.rep.Warnf("coordinator: could not journal %s leaving its hold: %v", harp, err)
		return
	}
	if emptied == nil {
		return
	}
	if l := c.holds[emptied.Key]; l != nil && l.id == emptied.ID {
		c.armLocked(emptied.Key, l, time.Time{})
		delete(c.holds, emptied.Key)
	}
}

// joinCredentialHold parks a run that has just come up on its credential's
// hold, when one is in force: a fresh run (a new child, or one relaunched
// after a stop) on a spent credential must wait with the others. The pause is
// sent under holdMu, so no release can resume the hold's runs ahead of it.
// The run's first turn may already be under way when it lands — that turn
// meets the limit and folds into the same hold.
func (c *Coordinator) joinCredentialHold(runID, harp string) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	at, key := c.now(), ""
	err := c.runs.Exec(func() ([]Fact, error) {
		l, ok := c.holdsF.launchOf(runID)
		if !ok || l.Source.Key == "" {
			return nil, nil
		}
		h := c.holdsF.byKey[l.Source.Key]
		if h == nil || h.Scope != holdScopeCredential {
			return nil, nil
		}
		if r := c.runsF.run(runID); r == nil || r.Ended || c.holdsF.holdOfRun(runID) != nil || c.holdsF.holdOfHarp(harp) != nil {
			return nil, nil
		}
		key = h.Key
		return []Fact{factAt(factHoldParked, at, holdParked{Key: key, RunID: runID, Harp: harp, Cause: "launch"})}, nil
	})
	if err != nil || key == "" {
		return
	}
	ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
	defer cancel()
	r := heldRun{runID, harp}
	if _, err := c.holdControl(ctx, r, "pause", "the credential it runs on is held"); err != nil {
		c.rep.Warnf("coordinator: could not park %s on its credential's hold: %v", harp, err)
		c.dropHarpLocked(harp, func(h *holdRecord) bool { return h.Key == key && h.Members[harp] == runID })
	}
}

// releaseHold is ControlResume's arm for a held harp: handled is false when
// no hold covers it. The human may release any hold; a failure's hold
// releases every harp in it. An agent may release only an agent's pause (one
// controlTarget let it control); a human's pause and a failure's hold refuse
// it. newly is the target run's own answer.
func (c *Coordinator) releaseHold(ctx context.Context, by ControlInitiator, rec *RunRecord) (handled, newly bool, err error) {
	var h holdRecord
	c.runs.View(func() {
		if r := c.holdsF.holdOfHarp(rec.Harp); r != nil {
			h = *r
		}
	})
	if h.Key == "" {
		return false, false, nil
	}
	switch {
	case by.Kind == InitiatorHuman:
	case h.Kind == HoldKindHuman:
		return true, false, fmt.Errorf("resume %s: %w", rec.Harp, ErrHumanPaused)
	case !h.pause():
		return true, false, fmt.Errorf("resume %s: %w", rec.Harp, ErrCredentialHeld)
	}
	c.holdMu.Lock()
	local := c.holds[h.Key]
	c.holdMu.Unlock()
	c.step(holdStepReleaseWait)
	if local != nil && local.id == h.ID {
		<-local.parked
	}
	cause := HoldKindHuman
	if by.Kind == InitiatorAgent {
		cause = HoldKindAgent
	}
	members, released := c.releaseKey(h.Key, h.ID, cause, by.auditName(), always)
	if !released {
		return true, false, nil // the backoff released it meanwhile
	}
	if o, ok := c.settleRelease(ctx, h.Key, members)[rec.RunID]; ok {
		return true, o.newly, o.err
	}
	return true, false, nil
}

// recordPause journals by's pause of rec's run as a hold of its own, unless a
// hold already parks that run (or covers its harp): then the runner's gate is
// already that hold's, and the pause only re-asserts it. id is the new hold's,
// "" when none was opened.
func (c *Coordinator) recordPause(by ControlInitiator, rec *RunRecord) (id string) {
	kind := HoldKindHuman
	if by.Kind == InitiatorAgent {
		kind = HoldKindAgent
	}
	key, at := pauseKey(rec.Harp), c.now()
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	err := c.runs.Exec(func() ([]Fact, error) {
		r := c.runsF.run(rec.RunID)
		if r == nil || r.Ended || c.holdsF.holdOfRun(rec.RunID) != nil || c.holdsF.holdOfHarp(rec.Harp) != nil {
			return nil, nil
		}
		id = RandID("hold-", 12)
		return []Fact{
			factAt(factHoldOpened, at, holdOpened{ID: id, Key: key, Scope: holdScopeRun, Kind: kind, By: by.auditName()}),
			factAt(factHoldParked, at, holdParked{Key: key, RunID: rec.RunID, Harp: rec.Harp, Cause: "pause"}),
		}, nil
	})
	if err != nil {
		c.rep.Warnf("coordinator: could not journal the pause of %s; it will not survive a restart: %v", rec.Harp, err)
		return ""
	}
	return id
}

// relaunchReleased relaunches harp, whose run ended while a hold covered it,
// once that hold has released — if mail waits for it and nothing else forbids
// it (an agent_stop, a drain, another hold).
func (c *Coordinator) relaunchReleased(harp string) {
	rec := c.currentRunRecord(harp)
	if rec == nil || !rec.Ended || rec.Cause == CauseStopped || rec.TopLevel() ||
		c.pendingCount(harp) == 0 || c.Draining() || c.harpHeld(harp) {
		return
	}
	attached := c.armLaunch(harp)
	c.goTracked(func() { c.resumeChild(harp, rec.RunID, attached, 0) })
}

// readoptHold is readopt's half for holds: a re-adopted run whose journal
// says it is held gets its pause re-sent, and one owed a released hold's
// resume gets that — each once its runner has registered (readopt runs
// inside RunnerHello, before the runner is reachable).
func (c *Coordinator) readoptHold(runID, harp, credHash string) {
	ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
	defer cancel()
	defer c.step(holdStepReasserted)
	if _, err := c.awaitRunner(ctx, credHash); err != nil {
		c.rep.Warnf("coordinator: %s's runner never registered; its hold was not re-asserted: %v", harp, err)
		return
	}
	var held bool
	var owedKey string
	var owed bool
	c.runs.View(func() {
		held = c.holdsF.holdOfRun(runID) != nil
		owedKey, owed = c.holdsF.owedOf(runID)
	})
	r := heldRun{runID, harp}
	switch {
	case held:
		if _, err := c.holdControl(ctx, r, "pause", "re-asserting its hold after a coordinator restart"); err != nil {
			c.rep.Warnf("coordinator: could not re-assert %s's hold: %v", harp, err)
		}
	case owed:
		_, _ = c.deliverOwedResume(ctx, owedKey, r)
	}
}

// adoptHolds rebuilds, at adoption, what no journal holds: each hold's timer.
// A hold whose deadline passed while the coordinator was down is released at
// once — journaled now; its runs' owed resumes go out as their runners
// re-Hello (readoptHold), since none can be reached yet. A hold with no
// deadline gets no timer. The human is told again of each hold still in force.
func (c *Coordinator) adoptHolds() {
	var holds []holdRecord
	c.runs.View(func() { holds = c.holdsF.inForce() })
	now := c.now()
	for _, h := range holds {
		switch {
		case h.Until.IsZero():
		case !h.Until.After(now):
			c.releaseKey(h.Key, h.ID, "backoff", "", always)
			continue
		default:
			parked := make(chan struct{})
			close(parked) // nothing is left to park in this process
			local := &holdLocal{id: h.ID, parked: parked}
			c.holdMu.Lock()
			c.holds[h.Key] = local
			c.armLocked(h.Key, local, h.Until)
			c.holdMu.Unlock()
		}
		if !h.pause() {
			until := "a human releases it"
			if !h.Until.IsZero() {
				until = h.Until.UTC().Format(time.RFC3339)
			}
			c.rep.Warnf("coordinator: a %s hold (%s) is still in force after a restart; its runs stay parked until %s",
				h.Kind, h.Key, until)
		}
	}
}
