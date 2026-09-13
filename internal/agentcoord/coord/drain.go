package coord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ---------------------------------------------------------------------------
// The bounded drain — ONE POLICY, TWO BOUNDS.
//
// Every wait the coordinator holds on a child is one of exactly two things:
//
//   - a wait on a PROCESS, which is BOUNDED at c.drainBound (agent_recv's own
//     maximum wait, mcpschema.RecvWaitMax — the one declaration of that
//     number). Exit is REQUESTED at drain start and FORCED at the bound;
//   - a wait on a HUMAN — a child parked in agent_recv or on a permission
//     decision (StateParked) — which is a PARK, not a wait: unbounded, never
//     forced, and LISTED so it cannot be forgotten.
//
// Expiry is LOUD: a forced child is audited, warned about, and reported to
// its parent as interrupted; a park is named in the outcome.
//
// The same loop serves TWO callers, under the same bound, and they differ
// only in drainPolicy: the coordinator's SHUTDOWN drain (BeginDrain — every
// child of every session, admission closed for good) and a session's bulk
// agent_stop (StopChildren — that session's own children, admission left
// open, because the sweep exists so the session can spawn again).
// ---------------------------------------------------------------------------

// Drain is one bounded drain's handle. Done closes once the drain has
// settled — every child it tracked has exited, been forced, or been
// classified as parked — and Outcome then says which.
type Drain struct {
	done chan struct{}
	// wake is poked (never blocked on) by terminateRun and setState so the
	// runner re-reads the folds the moment a child's state moves, rather
	// than polling or waiting out the bound for a child that already ended.
	wake    chan struct{}
	mu      sync.Mutex
	outcome DrainOutcome
	// tracked is the set this drain accounts for, fixed when it began.
	tracked []drainChild
	policy  drainPolicy
}

func newDrain(policy drainPolicy, tracked []drainChild) *Drain {
	return &Drain{done: make(chan struct{}), wake: make(chan struct{}, 1), tracked: tracked, policy: policy}
}

// drainPolicy is what separates the two drains: how a PARK is treated, and
// which terminal each way of ending records. The REQUEST/WAIT/FORCE loop and
// the bound are shared, so a policy cannot invent a second timeout.
type drainPolicy struct {
	// label names the drain in stderr prose.
	label string
	// parkIsWait: a child parked on a human is a PARK — unbounded, never
	// forced, listed in the outcome. The shutdown drain's rule. A bulk stop
	// ENDS a parked child instead: what it waits on is the very caller ending
	// it (an agent_recv on its parent) or a decision the caller is
	// overriding, and leaving it would leave the roster live and its
	// container up — exactly what the sweep exists to prevent.
	parkIsWait bool
	// endCause / endDetail record a child that ended WITHOUT a running turn
	// being cut short: between turns, before it started, at its turn
	// boundary, or while parked. `where` is that phrase.
	endCause  string
	endDetail func(where string) string
	// forceCause / forceDetail record a child still running at the bound.
	forceCause  string
	forceDetail func(bound time.Duration) string
}

// shutdownPolicy is BeginDrain's: the coordinator is going away.
func shutdownPolicy() drainPolicy {
	return drainPolicy{
		label:      "coordinator drain",
		parkIsWait: true,
		endCause:   CauseDrained,
		endDetail:  func(where string) string { return "coordinator drain: ended " + where },
		forceCause: CauseDrainInterrupted,
		forceDetail: func(bound time.Duration) string {
			return fmt.Sprintf("coordinator drain: turn still running after the %s bound; interrupted", bound)
		},
	}
}

// stopPolicy is StopChildren's: every end is an agent_stop by `caller` for
// `reason`, so the terminal cause is CauseStopped throughout (the roster,
// the parent's notice and the relaunch gate all treat it as the operator's
// own stop), and the detail says whether the turn was cut short.
func stopPolicy(caller, reason string) drainPolicy {
	by := fmt.Sprintf("stopped by %s: %s", caller, reason)
	return drainPolicy{
		label:      "agent_stop by " + caller,
		endCause:   CauseStopped,
		endDetail:  func(where string) string { return by + " (ended " + where + ")" },
		forceCause: CauseStopped,
		forceDetail: func(bound time.Duration) string {
			return fmt.Sprintf("%s (turn still running after the %s bound; interrupted)", by, bound)
		},
	}
}

// Done closes when the drain has settled.
func (d *Drain) Done() <-chan struct{} { return d.done }

// Outcome is the settled accounting; the zero value until Done closes.
func (d *Drain) Outcome() DrainOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.outcome
}

func (d *Drain) settle(outcome DrainOutcome) {
	d.mu.Lock()
	d.outcome = outcome
	d.mu.Unlock()
	close(d.done)
}

// DrainOutcome accounts for every child that was live when the drain began,
// by harp, each in exactly one list.
type DrainOutcome struct {
	// Exited ended inside the bound: at the turn boundary the drain asked
	// for, between turns, before ever starting, or by dying on their own.
	Exited []string
	// Interrupted were still running when the bound elapsed and were forced,
	// with the policy's forced cause as their terminal.
	Interrupted []string
	// Parked were waiting on a human when the drain settled and were left
	// exactly as they were: turn open, slot yielded, session lock held. The
	// caller decides whether to wait for the morning; nothing here does.
	Parked []string
}

// drainChild is one run the drain accounts for.
type drainChild struct{ harp, runID, agent string }

// drainTracked is the set a drain accounts for: every harp's current run that
// is live at the moment the drain begins, read from the folds (the
// authoritative state), never from the runtime attachment map. keep selects
// which of them; nil keeps every one (the shutdown drain).
func (c *Coordinator) drainTracked(keep func(*RunRecord) bool) []drainChild {
	var tracked []drainChild
	c.runs.View(func() {
		for _, e := range c.rosterF.snapshot() {
			r := c.runsF.currentRun(e.Harp)
			if r == nil || r.Ended || (keep != nil && !keep(r)) {
				continue
			}
			tracked = append(tracked, drainChild{harp: r.Harp, runID: r.RunID, agent: r.Agent})
		}
	})
	return tracked
}

// startDrain registers d as live and dispatches its runner. A drain is live
// from here until it settles, which is the window drainWake pokes it in.
func (c *Coordinator) startDrain(d *Drain) {
	c.drainMu.Lock()
	c.drains[d] = struct{}{}
	c.drainMu.Unlock()
	bound := c.drainBound
	c.goTracked(func() {
		c.runDrain(d, bound)
		c.drainMu.Lock()
		delete(c.drains, d)
		c.drainMu.Unlock()
	})
}

// drainWake pokes every live drain runner to re-read the folds.
func (c *Coordinator) drainWake() {
	c.drainMu.Lock()
	defer c.drainMu.Unlock()
	for d := range c.drains {
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
}

// runDrain is the bounded drain proper; startDrain dispatches it once per
// drain.
//
// REQUEST: every tracked child is classified from the folds. A child between
// turns or not yet started has nothing to wait for and ends now. A running
// turn is asked to exit — the ask is coordinator-side: the run is marked
// (childRt.exitRequested; for the shutdown drain the admission flag says the
// same of every run), and the turn-boundary handlers on both engine paths
// (onTurnBoundary, onTurnIdle) end a marked child instead of parking it idle.
// A parked child is left alone or ended, per the policy.
//
// WAIT: the runner re-reads the folds whenever a child moves and settles as
// soon as no child is still running. A child that parks mid-drain joins the
// parked list (or is ended, per the policy); one that unparks rejoins the
// wait, and its turn then ends at its boundary like any other.
//
// FORCE: when the bound elapses, every child still running is terminated the
// way agent_stop terminates one (KillRun semantics through terminateRun,
// whose engine close is itself graceful-then-hard) with the policy's forced
// cause, and the interruption is audited, warned, and reported to the parent.
func (c *Coordinator) runDrain(d *Drain, bound time.Duration) {
	p := d.policy
	for _, ch := range d.tracked {
		switch c.runState(ch.runID) {
		case StateParked:
			c.drainPark(ch, p)
		case StateExecuting:
			c.audit("drain_request", ch.harp, map[string]string{"harp": ch.harp, "run_id": ch.runID})
			c.requestExit(ch.runID, p)
		case StateIdle:
			c.terminateRun(ch.runID, p.endCause, p.endDetail("between turns"))
		default: // StateQueued: never admitted past the cap, so nothing to wait for
			c.terminateRun(ch.runID, p.endCause, p.endDetail("before it started"))
		}
	}

	timer := time.NewTimer(bound)
	defer timer.Stop()
	var exited, interrupted, parked []string
	for {
		// Re-classify the WHOLE tracked set every pass: a child may end,
		// park, or unpark between passes, and only its state now counts.
		var running []drainChild
		exited, parked = nil, nil
		for _, ch := range d.tracked {
			switch c.runState(ch.runID) {
			case StateEnded:
				exited = append(exited, ch.harp)
			case StateParked:
				if p.parkIsWait {
					parked = append(parked, ch.harp)
					continue
				}
				// Ended now; the next pass finds it in exited.
				c.drainPark(ch, p)
				running = append(running, ch)
			default:
				running = append(running, ch)
			}
		}
		if len(running) == 0 {
			break
		}
		select {
		case <-d.wake:
			continue
		case <-timer.C:
			for _, ch := range running {
				c.drainForce(ch.harp, ch.runID, bound, p)
				interrupted = append(interrupted, ch.harp)
			}
		case <-c.baseCtx.Done():
			// Close() overtook the drain: its attachment sweep kills what is
			// left. Account for them as what they are about to become.
			for _, ch := range running {
				interrupted = append(interrupted, ch.harp)
			}
		}
		break
	}

	outcome := DrainOutcome{
		Exited:      sortedCopy(exited),
		Interrupted: sortedCopy(interrupted),
		Parked:      sortedCopy(parked),
	}
	// Expiry is LOUD, and so is a park: a coordinator that goes quiet about
	// either is one whose operator finds out in the morning the hard way.
	if len(outcome.Interrupted) > 0 {
		clidiag.Warn("ctxloom", "%s: %d child(ren) still running after %s were interrupted: %s",
			p.label, len(outcome.Interrupted), bound, strings.Join(outcome.Interrupted, ", "))
	}
	if len(outcome.Parked) > 0 {
		clidiag.Warn("ctxloom", "%s: %d child(ren) are parked on a human and were left waiting (turn open, session lock held): %s",
			p.label, len(outcome.Parked), strings.Join(outcome.Parked, ", "))
	}
	d.settle(outcome)
}

// drainPark applies the policy to a parked child: a PARK is left exactly as
// it is (turn open, slot yielded, session lock held); otherwise the child is
// ended where it waits.
func (c *Coordinator) drainPark(ch drainChild, p drainPolicy) {
	if p.parkIsWait {
		return
	}
	c.terminateRun(ch.runID, p.endCause, p.endDetail("while parked"))
}

// requestExit marks a running child so its turn boundary ends it under p
// instead of parking it idle. A run with no attachment (a launch still in
// flight) cannot be marked, and need not be: its launch context was cancelled
// by the caller (cancelLaunch) or is about to be (Close), and it will not
// reach a boundary.
func (c *Coordinator) requestExit(runID string, p drainPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt := c.attach[runID]; rt != nil {
		rt.exitRequested = &p
	}
}

// exitRequested reports the policy under which rt must end at its turn
// boundary, or nil when it parks idle as usual. The shutdown drain's request
// is the admission flag (it covers every run, marked or not — a relaunch armed
// before the drain began and attached after its snapshot included); a bulk
// stop's is the per-run mark.
func (c *Coordinator) exitRequested(rt *childRt) *drainPolicy {
	if c.Draining() {
		p := shutdownPolicy()
		return &p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return rt.exitRequested
}

// drainForce is the FORCE half for one child: the run is terminated with the
// interruption named, and the fact is on the audit record beside the request
// that preceded it. terminateRun claims the terminal fact before it kills
// anything, so the cause the roster and the parent see is this one and not
// the chat-close/runner-exit the kill itself provokes; it also cancels the
// run's launch context, so an attempt still in flight turns back, and no
// relaunch can follow (launchgate.go, resumeChild).
func (c *Coordinator) drainForce(harp, runID string, bound time.Duration, p drainPolicy) {
	c.audit("drain_force", harp, map[string]string{"harp": harp, "run_id": runID, "bound": bound.String()})
	c.terminateRun(runID, p.forceCause, p.forceDetail(bound))
}

// drainAtBoundary is the REQUEST honoured: a child that reaches its turn
// boundary with an exit requested ends there, with its turn's result already
// bridged, instead of parking idle for a next turn that will never be handed
// out. A legacy child's input is closed FIRST so that engine sees
// end-of-input rather than a bare kill (onTurnBoundary runs on the driver
// goroutine, as closeChildInput requires; a migrated child has no input
// channel and the close is a no-op).
func (c *Coordinator) drainAtBoundary(rt *childRt, p *drainPolicy) {
	c.closeChildInput(rt)
	c.terminateRun(rt.runID, p.endCause, p.endDetail("at its turn boundary"))
}

// ---------------------------------------------------------------------------
// agent_stop's BULK form.
// ---------------------------------------------------------------------------

// ErrStopReasonRequired refuses a bulk stop with no reason: omitting run_id
// stops EVERY live child of the calling session, and an accidental omission
// must not do that silently.
var ErrStopReasonRequired = errors.New("agent_stop: reason is required when run_id is omitted (omitting run_id stops EVERY live child of this session; say why)")

// A StoppedChild's Outcome: the child ended without a turn being cut short
// (between turns, at its turn boundary, before it started, or while parked),
// or it was still running when the drain bound elapsed and was forced.
const (
	StopOutcomeStopped     = "stopped"
	StopOutcomeInterrupted = "interrupted"
)

// StoppedChild is one child's outcome from StopChildren.
type StoppedChild struct {
	Harp    string
	RunID   string
	Agent   string
	Outcome string
	// Detail is the run's terminal detail as the roster shows it, led by
	// its cause when that is not the stop itself (a child that died on its
	// own mid-sweep).
	Detail string
}

// StopChildren is agent_stop with run_id omitted: every live child of
// caller's session is stopped under the ONE drain bound (c.drainBound — the
// shutdown drain's, agent_recv's max wait; there is no second number), and
// the per-child outcome comes back by harp. It is addressed by lineage rather
// than by run_id on purpose: a resumed child runs under a FRESH run_id, so a
// run_id captured at spawn is stale after any resume, and the sweep exists so
// a coordinator never has to hand-roster the current ones.
//
// Each child is an agent_stop: the launch gate is marked stopped first (an
// armed relaunch turns back, leftover mail does not relaunch it — the mark
// only, not cancelLaunch's cancel, which on the legacy path would kill the
// very turn the REQUEST lets finish), the stop is audited per child with the
// reason, and the terminal is CauseStopped with the reason in its detail. The child stays resumable by an explicit
// agent_send, exactly as after the per-run form. Admission is NOT closed:
// this is a sweep so the session can spawn again, not the shutdown drain.
//
// The call returns when the sweep settles (at most the bound), or with
// ctx's error if the caller gives up first — the sweep itself carries on.
func (c *Coordinator) StopChildren(ctx context.Context, caller Identity, reason string) ([]StoppedChild, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ErrStopReasonRequired
	}
	tracked := c.drainTracked(func(r *RunRecord) bool { return r.ParentHarp == caller.Harp })
	for _, ch := range tracked {
		c.markStopped(ch.harp)
		c.audit("agent_stop", caller.Harp, map[string]string{"harp": ch.harp, "run_id": ch.runID, "reason": reason})
	}
	d := newDrain(stopPolicy(caller.Harp, reason), tracked)
	c.startDrain(d)
	select {
	case <-d.Done():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	outcome := d.Outcome()
	interrupted := make(map[string]bool, len(outcome.Interrupted))
	for _, h := range outcome.Interrupted {
		interrupted[h] = true
	}
	out := make([]StoppedChild, 0, len(tracked))
	for _, ch := range tracked {
		sc := StoppedChild{Harp: ch.harp, RunID: ch.runID, Agent: ch.agent, Outcome: StopOutcomeStopped}
		if interrupted[ch.harp] {
			sc.Outcome = StopOutcomeInterrupted
		}
		c.runs.View(func() {
			r := c.runsF.run(ch.runID)
			if r == nil {
				return
			}
			sc.Detail = r.Detail
			// A child that died on its own mid-sweep is reported as what it
			// was — the cause it died of leads — not repainted as a stop.
			if r.Cause != CauseStopped {
				sc.Detail = r.Cause + ": " + r.Detail
			}
		})
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harp < out[j].Harp })
	return out, nil
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
