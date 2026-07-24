package coord

import (
	"context"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Launch gating: the per-harp bookkeeping that makes a failing launch STOP.
//
// terminateRun's leftover-mail tail relaunches a harp whose mailbox is still
// non-empty — correct when the ENGINE died mid-conversation, but a hot
// unbounded loop when it is the LAUNCH that fails: the launch fails, the run
// ends, the mail is still queued (the child never came up to drain it), so a
// relaunch is armed at once, which fails, which re-arms. On 2026-07-24 this
// span for 49 minutes at roughly two container launches per second, and
// agent_stop could not stop it: the stop found an ENDED run (the loop's own
// terminal), reported success, and a relaunch already armed behind it minted
// a fresh run and carried on.
//
// Two things were missing and both live here:
//
//  1. A launch is CANCELLABLE. Every attempt runs under a per-harp context
//     that agent_stop cancels, so a stop reaches a launch that is in flight
//     (container prepare, image pull, fs probe — seconds of work) instead of
//     only the process a completed launch produced.
//  2. The retry is BOUNDED and BACKS OFF, and gives up LOUDLY. A launch that
//     has failed maxLaunchAttempts times will not succeed on the next one;
//     the whole 49-minute incident would have surfaced in seconds.

const (
	// maxLaunchAttempts bounds CONSECUTIVE failed launch attempts for one
	// harp before the coordinator gives up and says so. Deliberately small:
	// the failures this bounds (no image, unshared filesystem, no auth, no
	// reachable daemon) are all structural — they do not heal by attempt
	// N+1 — and every extra attempt costs a container prepare. An explicit
	// new delivery (agent_send/inject) resets the count: that is an
	// operator asking again, not the loop retrying itself.
	maxLaunchAttempts = 4
	// launchBackoffBase is the delay before the FIRST retry; each further
	// consecutive failure doubles it.
	launchBackoffBase = 200 * time.Millisecond
	// launchBackoffMax caps the doubling.
	launchBackoffMax = 30 * time.Second
)

// launchState is one harp's launch/retry bookkeeping, guarded by Coordinator.mu.
type launchState struct {
	// cancel cancels the in-flight attempt's launch context (nil when no
	// attempt is running); gen identifies WHICH attempt registered it, so a
	// finishing attempt cannot clear a racing successor's registration.
	cancel context.CancelFunc
	gen    uint64
	// fails counts CONSECUTIVE failed launch attempts; reset by a launch
	// that attaches, and by an explicit new delivery.
	fails int
	// stopped records an explicit agent_stop. It survives the run terminal
	// on purpose: the incident's stop landed on an ALREADY-ENDED run with a
	// relaunch armed behind it, and the run record alone could not express
	// "and do not bring it back". Cleared by an explicit new delivery, which
	// is the documented way a stopped child resumes.
	stopped bool
}

// launchGate returns harp's launch state, creating it on demand. Caller holds mu.
func (c *Coordinator) launchGateLocked(harp string) *launchState {
	if c.launches == nil {
		c.launches = map[string]*launchState{}
	}
	st := c.launches[harp]
	if st == nil {
		st = &launchState{}
		c.launches[harp] = st
	}
	return st
}

// launchContext derives the context ONE launch attempt runs under: a child of
// baseCtx, registered per-harp so agent_stop can cancel it mid-flight.
//
// The three return values separate two lifetimes that are NOT the same, and
// conflating them is a live-run kill switch:
//
//   - ctx / cancel belong to the ENGINE this launch produces. A spawner may
//     (and the coordinator's own runner half does) tie the engine's Home and
//     EngineHost to the context it was started under, so cancelling it the
//     moment the launch CALL returns tears down a perfectly healthy child.
//     Ownership therefore passes to the run: childRt.launchCancel, fired by
//     terminateRun. An attempt that never became a run cancels it itself.
//   - deregister only removes THIS attempt's per-harp registration (guarded
//     by gen, so a racing successor's is never clobbered). It cancels
//     nothing.
func (c *Coordinator) launchContext(harp string) (ctx context.Context, cancel context.CancelFunc, deregister func()) {
	ctx, cancel = context.WithCancel(c.baseCtx)
	c.mu.Lock()
	st := c.launchGateLocked(harp)
	st.gen++
	gen := st.gen
	st.cancel = cancel
	c.mu.Unlock()
	return ctx, cancel, func() {
		c.mu.Lock()
		if st.gen == gen {
			st.cancel = nil
		}
		c.mu.Unlock()
	}
}

// cancelLaunch is agent_stop's half: mark the harp stopped so no armed or
// future relaunch proceeds, and cancel any launch currently in flight. This
// is what makes a stop reach the LAUNCH and not merely the process — a
// container prepare is seconds long, and before this the stop simply raced it
// and lost.
func (c *Coordinator) cancelLaunch(harp string) {
	c.mu.Lock()
	st := c.launchGateLocked(harp)
	st.stopped = true
	cancel := st.cancel
	st.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// launchStopped reports whether an agent_stop has landed for harp since its
// last explicit delivery. Checked by resumeChild at every point it could
// still turn back — an attempt armed BEFORE the stop must not carry on
// behind it.
func (c *Coordinator) launchStopped(harp string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.launchGateLocked(harp)
	return st.stopped
}

// clearLaunchGate resets a harp's launch bookkeeping on an EXPLICIT new
// delivery (agent_send / inject to an ended child): a fresh ask from an
// operator or a parent is a fresh start, so it lifts a prior stop and clears
// the consecutive-failure count. Automatic relaunches never call this — that
// is what keeps the bound a bound.
func (c *Coordinator) clearLaunchGate(harp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.launchGateLocked(harp)
	st.stopped = false
	st.fails = 0
}

// noteLaunchAttached records that a launch actually came up: the
// consecutive-failure count resets, so a child that fails, recovers, and
// fails again much later gets a full budget rather than inheriting an old
// one.
func (c *Coordinator) noteLaunchAttached(harp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.launchGateLocked(harp).fails = 0
}

// noteLaunchFailure records one failed attempt and returns the new
// consecutive-failure count.
func (c *Coordinator) noteLaunchFailure(harp string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.launchGateLocked(harp)
	st.fails++
	return st.fails
}

// launchBackoff is the delay before the (fails+1)-th attempt: exponential
// from launchBackoffBase, capped at launchBackoffMax.
func launchBackoff(fails int) time.Duration {
	if fails <= 0 {
		return 0
	}
	d := launchBackoffBase
	for range fails - 1 {
		d *= 2
		if d >= launchBackoffMax {
			return launchBackoffMax
		}
	}
	return d
}

// nextRelaunch decides whether terminateRun's leftover-mail tail may relaunch
// harp, and after how long. It refuses once the harp has been stopped, or
// once maxLaunchAttempts consecutive attempts have failed.
func (c *Coordinator) nextRelaunch(harp string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.launchGateLocked(harp)
	if st.stopped || st.fails >= maxLaunchAttempts {
		return 0, false
	}
	return launchBackoff(st.fails), true
}

// relaunchForLeftoverMail is terminateRun's tail: a message that raced the
// child's death must not strand, so an ended harp with a non-empty mailbox is
// relaunched (§6a).
//
// It is ALSO the launch-retry loop, because a LAUNCH failure ends the run
// without ever draining the mailbox — so it re-arms itself immediately,
// forever, if nothing gates it. nextRelaunch is that gate: it refuses once
// the harp has been stopped or has burned maxLaunchAttempts consecutive
// failures, and otherwise returns the backoff this attempt must wait out.
// Exhausting the budget after a LAUNCH failure is reported loudly; every
// other cause simply stops re-arming (the child is not resumable by
// retrying, and its mail waits for an explicit delivery).
func (c *Coordinator) relaunchForLeftoverMail(rec RunRecord, cause, detail string) {
	if cause == CauseStopped || c.pendingCount(rec.Harp) == 0 {
		return
	}
	delay, ok := c.nextRelaunch(rec.Harp)
	if !ok {
		if cause == CauseLaunchFailed {
			c.giveUpLaunching(rec, detail)
		}
		return
	}
	attached := c.armLaunch(rec.Harp)
	c.goTracked(func() { c.resumeChild(rec.Harp, attached, delay) })
}

// giveUpLaunching is the LOUD end of a bounded retry: the parent's mailbox
// learns that the launcher stopped trying and why, and the fact goes to
// stderr too. The alternative — going quiet — is what let a broken launch
// look like a slow one for 49 minutes.
func (c *Coordinator) giveUpLaunching(rec RunRecord, detail string) {
	body := fmt.Sprintf("agent %q (session %s) failed to launch %d times in a row — giving up; "+
		"its queued messages are still waiting. Last failure: %s. "+
		"Fix the launch (image, runtime, auth, workspace) and agent_send again to retry.",
		rec.Agent, rec.Harp, maxLaunchAttempts, detail)
	clidiag.Warn("ctxloom", "%s", body)
	if rec.ParentHarp == "" {
		return
	}
	if _, _, err := c.queueMail(rec.Harp, rec.ParentHarp, "error", body); err != nil {
		clidiag.Warn("ctxloom", "agent %s: queue launch give-up notice: %v", rec.Harp, err)
	}
}

// sleepLaunchBackoff waits d, aborting early if the launch context is
// cancelled (agent_stop, or coordinator shutdown). Reports whether the wait
// completed and the attempt may proceed.
func sleepLaunchBackoff(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
