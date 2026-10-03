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
)

// A CREDENTIAL HOLD parks every run that shares a credential whose engine
// turned a turn away on a held failure (heldFailures). Without it each child
// meets the spent limit on its own next turn, consumes that turn's mail for
// nothing, and an unattended run thrashes one child at a time.
//
// The run whose turn was turned away has already parked itself (its runner
// raised the pause gate before reporting idle); the hold pauses the others
// with the same source (launch.Cell.Credential), raises ONE finding, and
// journals each run it parks. One hold per credential, with two ways out:
//   - its deadline (Until): a limit lifts on its own, so the hold releases
//     itself, resuming every run it parked;
//   - the HUMAN's resume of any held run releases all of them early.
//
// A limit still spent fails the next turn and parks again. State is in
// memory, like pausedRuns: holds are this coordinator's, shared by its
// children only.

// heldFailures are the turn failures a run parks itself on and its
// credential's hold releases. The runner parks only on these (HoldsFailure):
// a self-park on any other kind would be a pause nothing releases.
var heldFailures = []agent.FailureKind{agent.FailureRateLimited}

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
)

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

// CredentialHold is one held credential and the runs parked on it.
type CredentialHold struct {
	Engine engine.Name
	Source engine.CredentialSource
	Kind   agent.FailureKind
	Since  time.Time
	// Until is when the hold releases itself.
	Until time.Time
	// Harps are the runs this hold parked, sorted.
	Harps []string
}

// ErrCredentialHeld refuses an agent's resume of a run parked on its
// credential's hold: an agent that resumed it early would only meet the same
// limit, and re-raise the human's notice.
var ErrCredentialHeld = errors.New("coord: the run is parked on its credential's hold; only the human, or the hold's own backoff, releases it")

// credHold is a hold's state; guarded by Coordinator.mu.
type credHold struct {
	engine engine.Name
	source engine.CredentialSource
	kind   agent.FailureKind
	since  time.Time
	runs   map[string]string // run id → harp
	// until is the hold's deadline (zero once detached); stop disarms its
	// timer, and gen names the one armed timer whose firing still counts — a
	// Stop can lose to a callback already on its way.
	until time.Time
	stop  func() bool
	gen   int
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
)

// step reports a hold step to the test seam, when one is set.
func (c *Coordinator) step(name string) {
	if c.holdStep != nil {
		c.holdStep(name)
	}
}

// CredentialHolds is every hold in force, oldest first.
func (c *Coordinator) CredentialHolds() []CredentialHold {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CredentialHold, 0, len(c.credHolds))
	for _, h := range c.credHolds {
		out = append(out, CredentialHold{
			Engine: h.engine, Source: h.source, Kind: h.kind, Since: h.since, Until: h.until,
			Harps: slices.Sorted(maps.Values(h.runs)),
		})
	}
	slices.SortFunc(out, func(a, b CredentialHold) int {
		return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.Source.Key, b.Source.Key))
	})
	return out
}

// holdKey is the hold a run's source belongs to: its key, or — for a run that
// carries no credential — a hold of its own.
func holdKey(src engine.CredentialSource, runID string) string {
	if src.Key != "" {
		return src.Key
	}
	return "run:" + runID
}

// launchOf is the run's resolved launch (zero for a run with no plan).
func launchOf(rt *childRt) (engine.Name, engine.CredentialSource) {
	if rt.plan == nil {
		return "", engine.CredentialSource{}
	}
	return rt.plan.Launch.Engine, rt.plan.Launch.Cell.Credential
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

// foldFailureLocked folds one turned-away turn into h: its kind, and a
// deadline that only ever moves out — an earlier reset never shortens a wait
// another run's limit set.
func (c *Coordinator) foldFailureLocked(h *credHold, f agent.TurnFailure) {
	h.kind = f.Kind
	if d := backoffDeadline(c.now(), f.ResetsAt); d.After(h.until) {
		h.until = d
	}
}

// armLocked (re)arms h's timer for its deadline, or disarms it for none.
func (c *Coordinator) armLocked(key string, h *credHold) {
	if h.stop != nil {
		h.stop()
		h.stop = nil
	}
	h.gen++
	if h.until.IsZero() {
		return
	}
	gen := h.gen
	h.stop = c.afterFunc(h.until.Sub(c.now()), func() { c.backoffElapsed(key, h, gen) })
}

// backoffElapsed is a hold's own release: it counts only for the hold still in
// force under key, armed as gen.
func (c *Coordinator) backoffElapsed(key string, h *credHold, gen int) {
	if !c.detachParked(key, h, func() bool { return h.gen == gen }) {
		return
	}
	ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
	defer cancel()
	c.resumeHeld(ctx, key, h, "")
}

// detachParked takes h out of force once its parking has finished, if it is
// still the hold under key and still (under c.mu) wanted; false when another
// release, or a change of mind, got there first.
func (c *Coordinator) detachParked(key string, h *credHold, still func() bool) bool {
	c.step(holdStepReleaseWait)
	<-h.parked
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.credHolds[key] != h || !still() {
		return false
	}
	c.detachLocked(key, h)
	return true
}

// onTurnFailed folds a run's turned-away turn into its credential's hold:
// the run joins it (creating it if none is in force), and a new hold parks
// every other run with the same source. A run a HUMAN had already paused
// stays the human's — left out of the hold, so the release never undoes that
// pause — but its failure still parks its siblings: the limit is as spent
// for them.
func (c *Coordinator) onTurnFailed(role, runID string, f agent.TurnFailure) {
	c.mu.Lock()
	rt := c.runtimeForLocked(role, runID)
	if rt == nil {
		c.mu.Unlock()
		return
	}
	key, h, exists := c.holdForLocked(rt, runID)
	c.foldFailureLocked(h, f)
	_, joined := h.runs[runID]
	ownPark := !joined && !c.pausedByHumanLocked(runID)
	if ownPark {
		c.holdLocked(h, key, heldRun{runID, rt.harp})
	}
	var siblings []heldRun
	if !exists {
		siblings = c.siblingsLocked(h, key, runID)
	}
	c.settleLocked(key, h)
	until := h.until
	c.mu.Unlock()
	if ownPark {
		c.audit("credential_park", rt.harp, parkDetail(key, rt.harp, f.Kind, until))
	}
	if !exists {
		c.raiseHoldFinding(h.engine, key, until)
		c.goTracked(func() { c.parkSiblings(key, h, siblings) })
	}
	c.step(holdStepTurnFolded)
}

// holdForLocked is the hold rt's credential is under, made (not yet armed or
// parked) when none is in force; exists says which.
func (c *Coordinator) holdForLocked(rt *childRt, runID string) (key string, h *credHold, exists bool) {
	eng, src := launchOf(rt)
	key = holdKey(src, runID)
	if h, exists = c.credHolds[key]; !exists {
		h = &credHold{engine: eng, source: src, since: c.now(), runs: map[string]string{}, parked: make(chan struct{})}
		c.credHolds[key] = h
	}
	return key, h, exists
}

// settleLocked arms h for its deadline — or, when it holds no run, takes it
// out of force: nothing for it to park or release, as when the human's own
// pause already holds the one run that met the limit.
func (c *Coordinator) settleLocked(key string, h *credHold) {
	if len(h.runs) == 0 {
		delete(c.credHolds, key)
		h.until = time.Time{}
	}
	c.armLocked(key, h)
}

// parkDetail is the journal detail of a run whose own turn parked it.
func parkDetail(key, harp string, kind agent.FailureKind, until time.Time) map[string]string {
	d := map[string]string{"source": key, "kind": string(kind), "harp": harp, "cause": "turn"}
	if !until.IsZero() {
		d["until"] = until.UTC().Format(time.RFC3339)
	}
	return d
}

// raiseHoldFinding tells the root human what the hold is and when it ends; a
// zero until is a hold that parked nothing and is already out of force.
func (c *Coordinator) raiseHoldFinding(eng engine.Name, key string, until time.Time) {
	who := cmp.Or(string(eng), "the engine")
	if until.IsZero() {
		c.rep.Warnf("coordinator: %s's credential hit its rate limit (%s); no other run shares it, so nothing else is parked", who, key)
		return
	}
	c.rep.Warnf("coordinator: %s's credential hit its rate limit (%s); the runs sharing it are parked until %s and resume on their own",
		who, key, until.UTC().Format(time.RFC3339))
}

// pausedByHumanLocked: runID is paused, and not by a hold.
func (c *Coordinator) pausedByHumanLocked(runID string) bool {
	_, paused := c.pausedRuns[runID]
	_, held := c.heldRuns[runID]
	return paused && !held
}

// holdLocked records r as parked by h.
func (c *Coordinator) holdLocked(h *credHold, key string, r heldRun) {
	h.runs[r.runID] = r.harp
	c.heldRuns[r.runID] = key
	if c.pausedRuns == nil {
		c.pausedRuns = make(map[string]struct{})
	}
	c.pausedRuns[r.runID] = struct{}{}
}

// siblingsLocked registers, in h, every other attached run with h's source
// that a human has not paused — BEFORE its pause is sent, so a sibling whose
// own turn fails while that pause is in flight finds itself already held
// rather than reading the pause as a human's.
func (c *Coordinator) siblingsLocked(h *credHold, key, exceptRunID string) []heldRun {
	var out []heldRun
	for id, rt := range c.attach {
		if id == exceptRunID || c.pausedByHumanLocked(id) {
			continue
		}
		if _, src := launchOf(rt); holdKey(src, id) != key {
			continue
		}
		r := heldRun{id, rt.harp}
		c.holdLocked(h, key, r)
		out = append(out, r)
	}
	return out
}

// parkSiblings pauses each registered sibling at its runner, then marks h's
// parking done. One that cannot be paused (ended, or its runner gone) leaves
// the hold.
func (c *Coordinator) parkSiblings(key string, h *credHold, siblings []heldRun) {
	defer close(h.parked)
	for _, r := range siblings {
		c.step(holdStepParkSibling)
		ctx, cancel := context.WithTimeout(c.baseCtx, DefaultRequestTimeout)
		err := c.holdControl(ctx, r, "pause", "the credential it shares hit its rate limit")
		cancel()
		if err != nil {
			c.dropFromHold(key, h, r.runID)
			c.rep.Warnf("coordinator: could not park %s on its credential's hold: %v", r.harp, err)
			continue
		}
		c.audit("credential_park", r.harp, map[string]string{"source": key, "harp": r.harp, "cause": "sibling"})
	}
}

// holdControl sends a hold's pause or resume to r's runner, as the
// coordinator itself: no initiator's ownership applies.
func (c *Coordinator) holdControl(ctx context.Context, r heldRun, verb, reason string) error {
	rec := c.currentRunRecord(r.harp)
	if rec == nil || rec.RunID != r.runID || rec.Ended {
		return fmt.Errorf("run %s of %q is no longer current", r.runID, r.harp)
	}
	_, err := c.sendRunnerControl(ctx, rec, verb, reason)
	return err
}

// dropFromHold removes runID from h, and h from force if that empties it. h
// is still in force: no release detaches a hold before its parking is done.
func (c *Coordinator) dropFromHold(key string, h *credHold, runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(h.runs, runID)
	delete(c.pausedRuns, runID)
	delete(c.heldRuns, runID)
	if len(h.runs) == 0 {
		c.detachLocked(key, h)
	}
}

// detachLocked takes h out of force: its runs are no longer held by it and its
// timer is disarmed. Their pauses stand until resumeHeld lifts them.
func (c *Coordinator) detachLocked(key string, h *credHold) {
	delete(c.credHolds, key)
	for id := range h.runs {
		delete(c.heldRuns, id)
	}
	h.until = time.Time{}
	c.armLocked(key, h)
}

// resumeHeld resumes every run a detached hold parked, journaling each, and
// returns each run's outcome. actor is the human for their resume, "" for the
// backoff's own release.
func (c *Coordinator) resumeHeld(ctx context.Context, key string, h *credHold, actor string) map[string]error {
	c.mu.Lock()
	runs := maps.Clone(h.runs)
	c.mu.Unlock()
	out := make(map[string]error, len(runs))
	for _, id := range slices.Sorted(maps.Keys(runs)) {
		r := heldRun{id, runs[id]}
		err := c.holdControl(ctx, r, "resume", "")
		out[id] = err
		switch {
		case err != nil:
			c.rep.Warnf("coordinator: could not resume %s from its credential's hold: %v", r.harp, err)
		case actor == "":
			c.audit("credential_resume", r.harp, map[string]string{"source": key, "harp": r.harp, "cause": "backoff"})
		default:
			c.audit("credential_resume", actor, map[string]string{"source": key, "harp": r.harp})
		}
	}
	return out
}

// releaseHold is ControlResume's arm for a held run: handled is false when
// rec is in no hold. An agent is refused; the human's resume releases every
// run in the hold, and newly is the target's own answer.
func (c *Coordinator) releaseHold(ctx context.Context, by ControlInitiator, rec *RunRecord) (handled, newly bool, err error) {
	c.mu.Lock()
	key, held := c.heldRuns[rec.RunID]
	if !held {
		c.mu.Unlock()
		return false, false, nil
	}
	if by.Kind != InitiatorHuman {
		c.mu.Unlock()
		return true, false, fmt.Errorf("resume %s: %w", rec.Harp, ErrCredentialHeld)
	}
	h := c.credHolds[key]
	c.mu.Unlock()
	if !c.detachParked(key, h, func() bool { return true }) {
		return true, false, nil // the backoff released it meanwhile
	}
	outcomes := c.resumeHeld(ctx, key, h, UserSender)
	if err, ok := outcomes[rec.RunID]; ok {
		return true, err == nil, err
	}
	return true, false, nil
}
