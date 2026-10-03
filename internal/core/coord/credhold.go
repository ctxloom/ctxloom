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

// The kinds of a pause hold, by who paused (ControlInitiator.Kind). A
// failure's hold carries its agent.FailureKind.
const (
	holdKindHuman = "human"
	holdKindAgent = "agent"
)

// heldFailures are the turn failures a run parks itself on and its
// credential's hold releases. The runner parks only on these (HoldsFailure):
// a self-park on any other kind would be a pause nothing releases.
var heldFailures = []agent.FailureKind{agent.FailureRateLimited, agent.FailureOverloaded}

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
			if h.pause() {
				continue
			}
			out = append(out, CredentialHold{
				Engine: h.Engine, Source: h.Source, Kind: agent.FailureKind(h.Kind), Since: h.Since, Until: h.Until,
				Harps: slices.Sorted(maps.Keys(h.Members)),
			})
		}
	})
	return out
}

// runHolds is the roster's view of every run a failure's hold parks, by run
// id. Its Source is the credential's carrier names, not the hold's key: a
// run's own hold (an overload, or a run with no credential) is keyed by its
// run id, which names no carrier.
func (c *Coordinator) runHolds() map[string]*RunHold {
	out := make(map[string]*RunHold)
	c.runs.View(func() {
		for runID := range c.holdsF.byRun {
			if h := c.holdsF.holdOfRun(runID); !h.pause() {
				out[runID] = &RunHold{Kind: h.Kind, Source: h.Source.Key, Until: h.Until}
			}
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

// launchOf is the run's resolved launch (zero for a run with no plan).
func launchOf(rt *childRt) (engine.Name, engine.CredentialSource) {
	if rt.plan == nil {
		return "", engine.CredentialSource{}
	}
	return rt.plan.Launch.Engine, rt.plan.Launch.Cell.Credential
}

// recordLaunch journals a run's resolved engine and credential source, the
// identity a re-adopted run is keyed by (readopt).
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
	c.mu.Lock()
	rt := c.runtimeForLocked(role, runID)
	if rt == nil {
		c.mu.Unlock()
		return
	}
	eng, src := launchOf(rt)
	harp := rt.harp
	key, scope := holdKey(f.Kind, src, runID), holdScopeOf(f.Kind, src)
	var candidates []heldRun
	for id, srt := range c.attach {
		if _, s := launchOf(srt); id != runID && holdKey(f.Kind, s, id) == key {
			candidates = append(candidates, heldRun{id, srt.harp})
		}
	}
	c.mu.Unlock()
	slices.SortFunc(candidates, func(a, b heldRun) int { return cmp.Compare(a.runID, b.runID) })

	c.holdMu.Lock()
	// A hold that may open here gets its timer armed BEFORE it is journaled,
	// so no reader ever sees a hold in force with no timer to release it.
	// Which holds are in force changes only under holdMu, so whether this one
	// opens is already settled; whether it parks anyone is decide's to say,
	// and a hold that opens nothing is disarmed again below.
	now := c.now()
	var local *holdLocal
	c.runs.View(func() {
		if c.holdsF.byKey[key] == nil {
			local = &holdLocal{id: RandID("hold-", 12), parked: make(chan struct{})}
		}
	})
	if local != nil {
		c.holds[key] = local
		c.armLocked(key, local, holdDeadline(now, f))
	}
	var d turnFold
	err := c.runs.Exec(func() ([]Fact, error) {
		var facts []Fact
		d, facts = c.decideTurnFailure(f, now, local, key, scope, eng, src, heldRun{runID, harp}, candidates)
		return facts, nil
	})
	switch {
	case local != nil && (err != nil || !d.opened):
		c.armLocked(key, local, time.Time{})
		delete(c.holds, key)
	case err == nil && d.moved:
		if l := c.holds[key]; l != nil {
			c.armLocked(key, l, d.until)
		}
	}
	c.holdMu.Unlock()
	switch {
	case err != nil:
		c.rep.Warnf("coordinator: could not journal %s's turn failure into its hold: %v", harp, err)
	case d.opened:
		c.raiseHoldFinding(eng, f.Kind, key, harp, d.until, true)
		c.goTracked(func() { c.parkSiblings(key, local, d.siblings) })
	case d.parkedNothing:
		c.raiseHoldFinding(eng, f.Kind, key, harp, time.Time{}, false)
	}
}

// decideTurnFailure is onTurnFailed's decision, inside the run journal's
// Exec: it reads only the folds. local is the hold onTurnFailed armed for a
// key with none in force (nil when one is).
func (c *Coordinator) decideTurnFailure(f agent.TurnFailure, now time.Time, local *holdLocal, key string, scope holdScope, eng engine.Name, src engine.CredentialSource, own heldRun, candidates []heldRun) (turnFold, []Fact) {
	var d turnFold
	if r := c.runsF.run(own.runID); r == nil || r.Ended {
		return d, nil // a turn boundary after the run's terminal folds nothing
	}
	other := c.holdsF.holdOfRun(own.runID)
	if other != nil && other.Key != key && !other.pause() {
		return d, nil
	}
	ownPark := other == nil
	rec := c.holdsF.byKey[key]
	if rec == nil {
		for _, s := range candidates {
			if r := c.runsF.run(s.runID); r != nil && !r.Ended && c.holdsF.holdOfRun(s.runID) == nil {
				d.siblings = append(d.siblings, s)
			}
		}
		if !ownPark && len(d.siblings) == 0 {
			d.parkedNothing = true
			return d, nil
		}
		if local == nil {
			return turnFold{}, nil // unreachable: holds come into force only under holdMu
		}
		d.opened, d.until = true, holdDeadline(now, f)
		facts := []Fact{factAt(factHoldOpened, now, holdOpened{ID: local.id, Key: key, Scope: scope, Kind: string(f.Kind), Engine: eng, Source: src, Until: d.until})}
		if ownPark {
			facts = append(facts, factAt(factHoldParked, now, holdParked{Key: key, RunID: own.runID, Harp: own.harp, Cause: "turn"}))
		}
		for _, s := range d.siblings {
			facts = append(facts, factAt(factHoldParked, now, holdParked{Key: key, RunID: s.runID, Harp: s.harp, Cause: "sibling"}))
		}
		return d, facts
	}
	var facts []Fact
	// A deadline only ever moves out — an earlier reset never shortens a wait
	// another run's limit set — and a hold with NO deadline is not given one,
	// or relabelled, by a later failure.
	if !rec.Until.IsZero() {
		d.until = rec.Until
		if dl := holdDeadline(now, f); dl.After(rec.Until) {
			d.until, d.moved = dl, true
		}
		if d.moved || rec.Kind != string(f.Kind) {
			facts = append(facts, factAt(factHoldExtended, now, holdExtended{Key: key, Kind: string(f.Kind), Until: d.until}))
		}
	}
	if ownPark {
		facts = append(facts, factAt(factHoldParked, now, holdParked{Key: key, RunID: own.runID, Harp: own.harp, Cause: "turn"}))
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
// re-sends it once the runner is back (reassertHold).
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
	at := c.now()
	emptied := false
	err := c.runs.Exec(func() ([]Fact, error) {
		h := c.holdsF.byKey[key]
		if h == nil || h.ID != id || h.Members[r.harp] != r.runID {
			return nil, nil
		}
		facts := []Fact{factAt(factHoldDropped, at, holdDropped{Key: key, RunID: r.runID, Harp: r.harp})}
		if len(h.Members) == 1 {
			emptied = true
			facts = append(facts, factAt(factHoldReleased, at, holdReleased{Key: key, Cause: "empty"}))
		}
		return facts, nil
	})
	if err != nil {
		c.rep.Warnf("coordinator: could not journal %s leaving hold %s: %v", r.harp, key, err)
		return
	}
	if l := c.holds[key]; emptied && l != nil && l.id == id {
		c.armLocked(key, l, time.Time{})
		delete(c.holds, key)
	}
}

// releaseHold is ControlResume's arm for a held harp: handled is false when
// no hold covers it. A failure's hold refuses an agent; the human's resume
// releases every harp in it. A pause is released by whoever may control the
// run. newly is the target run's own answer.
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
	if !h.pause() && by.Kind != InitiatorHuman {
		return true, false, fmt.Errorf("resume %s: %w", rec.Harp, ErrCredentialHeld)
	}
	c.holdMu.Lock()
	local := c.holds[h.Key]
	c.holdMu.Unlock()
	c.step(holdStepReleaseWait)
	if local != nil && local.id == h.ID {
		<-local.parked
	}
	cause := holdKindHuman
	if by.Kind == InitiatorAgent {
		cause = holdKindAgent
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
	kind := holdKindHuman
	if by.Kind == InitiatorAgent {
		kind = holdKindAgent
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
