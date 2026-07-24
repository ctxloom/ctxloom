package coord

import (
	"context"
	"fmt"
	"os"
	"strconv"
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
	// defaultMaxLaunchAttempts bounds CONSECUTIVE failed launch attempts for
	// one harp before the coordinator gives up and says so. Deliberately
	// small: the failures this bounds (no image, unshared filesystem, no
	// auth, no reachable daemon) are all structural — they do not heal by
	// attempt N+1 — and every extra attempt costs a container prepare. An
	// explicit new delivery (agent_send/inject) resets the count: that is an
	// operator asking again, not the loop retrying itself.
	//
	// This is the DEFAULT only — EnvLaunchMaxAttempts overrides it per
	// process. Nobody signed off on "4" as exactly right (lunar-boat item 1);
	// the env override exists so it can be tuned without a rebuild once real
	// container-daemon behavior is observed.
	defaultMaxLaunchAttempts = 4
	// defaultLaunchBackoffBase is the delay before the FIRST retry; each
	// further consecutive failure doubles it. DEFAULT only — see
	// EnvLaunchBackoffBase.
	defaultLaunchBackoffBase = 200 * time.Millisecond
	// defaultLaunchBackoffMax caps the doubling. DEFAULT only — see
	// EnvLaunchBackoffMax.
	defaultLaunchBackoffMax = 30 * time.Second
)

// Env var names overriding the launch-retry budget's built-in defaults
// above. Each is read ONCE, at Coordinator construction (resolveLaunchTunables,
// called from New) — never per attempt inside the retry loop itself, which
// only ever reads the already-resolved Coordinator fields
// (maxLaunchAttempts/launchBackoffBase/launchBackoffMax). Matches the
// existing CTXLOOM_* env convention (e.g. CTXLOOM_VERBOSE).
const (
	// EnvLaunchMaxAttempts overrides defaultMaxLaunchAttempts: the number of
	// CONSECUTIVE failed launch attempts, as a positive integer (e.g. "8"),
	// the coordinator tolerates for one harp before giving up loudly.
	// Default: defaultMaxLaunchAttempts (4). Raise it to ride out a slow or
	// cold container daemon so a transient failure gets more chances to
	// clear before the coordinator gives up; lower it to fail faster and
	// surface a genuinely broken launch (bad image, no auth, unreachable
	// daemon) sooner. An unset, empty, non-positive, or unparseable value
	// falls back to the default WITH A LOUD WARNING — never to zero, which
	// would resurrect the pre-fix unbounded-retry spin (49 minutes at ~2
	// launches/sec, 2026-07-24).
	EnvLaunchMaxAttempts = "CTXLOOM_LAUNCH_MAX_ATTEMPTS"
	// EnvLaunchBackoffBase overrides defaultLaunchBackoffBase: the delay
	// before the FIRST retry, Go duration syntax (e.g. "500ms"); each
	// further consecutive failure doubles it. Default:
	// defaultLaunchBackoffBase (200ms). Raise it to space attempts out
	// further against a daemon that is slow to recover; lower it to retry a
	// genuinely transient failure sooner. Same non-positive/unparseable
	// fallback as EnvLaunchMaxAttempts.
	EnvLaunchBackoffBase = "CTXLOOM_LAUNCH_BACKOFF_BASE"
	// EnvLaunchBackoffMax overrides defaultLaunchBackoffMax: the ceiling the
	// doubling backoff is capped at, Go duration syntax (e.g. "1m").
	// Default: defaultLaunchBackoffMax (30s). Raise it to let the backoff
	// keep growing across a longer cold-daemon recovery; lower it to keep
	// the operator-visible give-up closer to the attempt budget's floor.
	// Same non-positive/unparseable fallback as EnvLaunchMaxAttempts.
	EnvLaunchBackoffMax = "CTXLOOM_LAUNCH_BACKOFF_MAX"
)

// envLaunchInt resolves name from the environment as a positive integer,
// falling back to def with a loud warning when the variable is SET but
// unparseable or non-positive (zero included — a zero attempt budget is the
// exact defect class this whole gate exists to prevent). An unset or
// empty-string variable falls back to def SILENTLY: that is the ordinary,
// unconfigured case, not an operator error.
func envLaunchInt(name string, def int) int {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		clidiag.Warn("ctxloom", "%s=%q is not a positive integer; using the default %d instead (a zero or negative value here would silently reopen the unbounded-retry bug this budget exists to close)", name, raw, def)
		return def
	}
	return n
}

// envLaunchDuration is envLaunchInt's Go-duration-syntax sibling (e.g.
// "500ms", "30s").
func envLaunchDuration(name string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		clidiag.Warn("ctxloom", "%s=%q is not a positive duration; using the default %s instead (a zero backoff here would silently reopen the unbounded-retry bug this budget exists to close)", name, raw, def)
		return def
	}
	return d
}

// resolveLaunchTunables reads the container launch-retry budget's operator
// overrides from the environment. Called exactly ONCE, from New, at
// Coordinator construction — the point the policy is established — and the
// result is cached on the Coordinator's maxLaunchAttempts/launchBackoffBase/
// launchBackoffMax fields, which is what every per-attempt read (launchBackoff,
// nextRelaunch, giveUpLaunching) actually consults. No env var is read inside
// the retry loop itself.
func resolveLaunchTunables() (maxAttempts int, backoffBase, backoffMax time.Duration) {
	maxAttempts = envLaunchInt(EnvLaunchMaxAttempts, defaultMaxLaunchAttempts)
	backoffBase = envLaunchDuration(EnvLaunchBackoffBase, defaultLaunchBackoffBase)
	backoffMax = envLaunchDuration(EnvLaunchBackoffMax, defaultLaunchBackoffMax)
	return
}

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
// from c.launchBackoffBase, capped at c.launchBackoffMax — the resolved
// per-Coordinator values (defaults, or EnvLaunchBackoffBase/EnvLaunchBackoffMax
// overrides, resolved once at construction; see resolveLaunchTunables).
func (c *Coordinator) launchBackoff(fails int) time.Duration {
	if fails <= 0 {
		return 0
	}
	d := c.launchBackoffBase
	for range fails - 1 {
		d *= 2
		if d >= c.launchBackoffMax {
			return c.launchBackoffMax
		}
	}
	return d
}

// nextRelaunch decides whether terminateRun's leftover-mail tail may relaunch
// harp, and after how long. It refuses once the harp has been stopped, or
// once c.maxLaunchAttempts consecutive attempts have failed.
func (c *Coordinator) nextRelaunch(harp string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.launchGateLocked(harp)
	if st.stopped || st.fails >= c.maxLaunchAttempts {
		return 0, false
	}
	return c.launchBackoff(st.fails), true
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
		rec.Agent, rec.Harp, c.maxLaunchAttempts, detail)
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
