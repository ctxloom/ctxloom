package coord

import (
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
// ---------------------------------------------------------------------------

// Drain is BeginDrain's handle. Done closes once the drain has settled —
// every child that was live when it began has exited, been forced, or been
// classified as parked — and Outcome then says which.
type Drain struct {
	done chan struct{}
	// wake is poked (never blocked on) by terminateRun and setState so the
	// runner re-reads the folds the moment a child's state moves, rather
	// than polling or waiting out the bound for a child that already ended.
	wake    chan struct{}
	mu      sync.Mutex
	outcome DrainOutcome
}

func newDrain() *Drain {
	return &Drain{done: make(chan struct{}), wake: make(chan struct{}, 1)}
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
	// Interrupted were still running when the bound elapsed and were forced.
	// Their terminal cause is CauseDrainInterrupted.
	Interrupted []string
	// Parked were waiting on a human when the drain settled and were left
	// exactly as they were: turn open, slot yielded, session lock held. The
	// caller decides whether to wait for the morning; nothing here does.
	Parked []string
}

// drainChild is one run the drain accounts for.
type drainChild struct{ harp, runID string }

// drainTracked is the set a drain accounts for: every harp's current run that
// is live at the moment BeginDrain is called, read from the folds (the
// authoritative state), never from the runtime attachment map.
func (c *Coordinator) drainTracked() []drainChild {
	var tracked []drainChild
	c.runs.View(func() {
		for _, e := range c.rosterF.snapshot() {
			r := c.runsF.currentRun(e.Harp)
			if r == nil || r.Ended {
				continue
			}
			tracked = append(tracked, drainChild{harp: r.Harp, runID: r.RunID})
		}
	})
	return tracked
}

// drainWake pokes the drain runner, if one is live, to re-read the folds.
func (c *Coordinator) drainWake() {
	c.drainMu.Lock()
	d := c.drain
	c.drainMu.Unlock()
	if d == nil {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// runDrain is the bounded drain proper; BeginDrain dispatches it once.
//
// REQUEST: every live child is classified from the folds. A child between
// turns or not yet started has nothing to wait for and ends now. A running
// turn is asked to exit — the ask is coordinator-side: no new turn is handed
// out once admission is closed, and the turn-boundary handlers on both engine
// paths (onTurnBoundary, onTurnIdle) end a child instead of parking it idle
// while the coordinator is draining. A parked child is not asked anything.
//
// WAIT: the runner re-reads the folds whenever a child moves and settles as
// soon as no child is still running. A child that parks mid-drain joins the
// parked list; one that unparks rejoins the wait, and its turn then ends at
// its boundary like any other.
//
// FORCE: when the bound elapses, every child still running is terminated the
// way agent_stop terminates one (KillRun semantics through terminateRun,
// whose engine close is itself graceful-then-hard) with CauseDrainInterrupted,
// and the interruption is audited, warned, and reported to the parent.
func (c *Coordinator) runDrain(d *Drain, tracked []drainChild, bound time.Duration) {
	for _, ch := range tracked {
		switch c.runState(ch.runID) {
		case StateParked:
			// A park is a wait on a human: not requested, not bounded.
		case StateExecuting:
			c.audit("drain_request", ch.harp, map[string]string{"harp": ch.harp, "run_id": ch.runID})
		case StateIdle:
			c.terminateRun(ch.runID, CauseDrained, "coordinator drain: ended between turns")
		default: // StateQueued: never admitted past the cap, so nothing to wait for
			c.terminateRun(ch.runID, CauseDrained, "coordinator drain: ended before it started")
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
		for _, ch := range tracked {
			switch c.runState(ch.runID) {
			case StateEnded:
				exited = append(exited, ch.harp)
			case StateParked:
				parked = append(parked, ch.harp)
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
				c.drainForce(ch.harp, ch.runID, bound)
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
		clidiag.Warn("ctxloom", "coordinator drain: %d child(ren) still running after %s were interrupted: %s",
			len(outcome.Interrupted), bound, strings.Join(outcome.Interrupted, ", "))
	}
	if len(outcome.Parked) > 0 {
		clidiag.Warn("ctxloom", "coordinator drain: %d child(ren) are parked on a human and were left waiting (turn open, session lock held): %s",
			len(outcome.Parked), strings.Join(outcome.Parked, ", "))
	}
	d.settle(outcome)
}

// drainForce is the FORCE half for one child: the run is terminated with the
// interruption named, and the fact is on the audit record beside the request
// that preceded it. terminateRun claims the terminal fact before it kills
// anything, so the cause the roster and the parent see is this one and not
// the chat-close/runner-exit the kill itself provokes; it also cancels the
// run's launch context, so an attempt still in flight turns back, and no
// relaunch can follow under drain (launchgate.go, resumeChild).
func (c *Coordinator) drainForce(harp, runID string, bound time.Duration) {
	c.audit("drain_force", harp, map[string]string{"harp": harp, "run_id": runID, "bound": bound.String()})
	c.terminateRun(runID, CauseDrainInterrupted,
		fmt.Sprintf("coordinator drain: turn still running after the %s bound; interrupted", bound))
}

// drainAtBoundary is the REQUEST honoured: a child that reaches its turn
// boundary while the coordinator is draining ends there, with its turn's
// result already bridged, instead of parking idle for a next turn that will
// never be handed out. A legacy child's input is closed FIRST so that engine
// sees end-of-input rather than a bare kill (onTurnBoundary runs on the
// driver goroutine, as closeChildInput requires; a migrated child has no
// input channel and the close is a no-op).
func (c *Coordinator) drainAtBoundary(rt *childRt) {
	c.closeChildInput(rt)
	c.terminateRun(rt.runID, CauseDrained, "coordinator drain: ended at its turn boundary")
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
