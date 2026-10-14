package coord

import (
	"context"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A heartbeat watchdog that was itself not running cannot read runner silence
// as runner death. In the verprop incident (2026-10-13) an OOM storm stalled
// the whole host for about two minutes; when the coordinator resumed, its
// watchdog found every runner silent past the loss bound and ended all three
// runs, the session owner's own among them, though the runners were alive
// and reconnecting. A tick that arrives late by more than watchdogStallLateness
// is that case: every runner gets one fresh loss bound from now, and a runner
// that really died is still ended one bound later.

// stallRig is one live child whose runner is connected, under a fake clock the
// test advances tick by tick.
type stallRig struct {
	c     *Coordinator
	clock *chanClock
	run   *RunOutcome
	link  TestRunnerLink
}

func newStallRig(t *testing.T) *stallRig {
	t.Helper()
	resetStrictness(t)
	clock := &chanClock{}
	clock.set(time.Unix(1_700_000_000, 0))
	gate := make(chan struct{})
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, clock.now)

	run, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := sp.chat(0)
		return e != nil && len(e.RecordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	link := dialFakeRunner(t, c, run.RunID)
	t.Cleanup(link.Abort)
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.runners) == 1
	}, conformanceWait, 10*time.Millisecond)
	return &stallRig{c: c, clock: clock, run: run, link: link}
}

// tick runs the watchdog body at the given time, the clock moved there first
// so anything stamped meanwhile is stamped "now".
func (r *stallRig) tick(at time.Time) {
	r.clock.set(at)
	r.c.checkRunnerLiveness(at)
}

func (r *stallRig) ended() bool { return rosterState(r.c, r.run.Harp) == StateEnded }

func TestWatchdog_LateTickExtendsTheLossBoundInsteadOfEndingRuns(t *testing.T) {
	r := newStallRig(t)
	t0 := r.clock.now()
	r.tick(t0) // the watchdog's last on-time tick

	// The host stalls: the next tick arrives long past the loss bound.
	resumed := t0.Add(runnerLossTimeout + 2*time.Minute)
	r.tick(resumed)
	assert.Never(t, r.ended, 200*time.Millisecond, 10*time.Millisecond,
		"a run was ended on silence the watchdog's own stall accounts for")

	// On-time ticks within the fresh bound end nothing either.
	at := resumed
	for at.Add(HeartbeatInterval).Sub(resumed) <= runnerLossTimeout {
		at = at.Add(HeartbeatInterval)
		r.tick(at)
	}
	assert.False(t, r.ended(), "the fresh bound is a whole loss bound")

	// The runner is still silent: the next on-time tick past the fresh bound
	// ends it, as it would have ended any runner that genuinely died.
	r.tick(at.Add(HeartbeatInterval))
	require.Eventually(t, r.ended, conformanceWait, 10*time.Millisecond,
		"a runner still silent one fresh bound after the stall is lost")
}

func TestWatchdog_OnTimeTicksStillEndASilentRunner(t *testing.T) {
	// The grace is for a late tick only: on schedule, silence past the bound
	// is loss, exactly as before.
	r := newStallRig(t)
	t0 := r.clock.now()
	for at := t0; at.Sub(t0) <= runnerLossTimeout+HeartbeatInterval; at = at.Add(HeartbeatInterval) {
		r.tick(at)
	}
	require.Eventually(t, r.ended, conformanceWait, 10*time.Millisecond)
}

func TestWatchdog_LateTickIsJournaled(t *testing.T) {
	r := newStallRig(t)
	t0 := r.clock.now()
	r.tick(t0)
	r.tick(t0.Add(runnerLossTimeout + 2*time.Minute))

	entries := readAuditKind(t, r.c, "watchdog_stall")
	require.Len(t, entries, 1, "the grace is journaled, so a reader can see why the runs survived")
	found := entries[0].Detail
	assert.Contains(t, found["detail"], "host stall suspected")
	assert.Equal(t, (runnerLossTimeout + 2*time.Minute - HeartbeatInterval).String(), found["late_by"])
}

// TestWatchdog_StallThresholdIsOneMissedTick pins the documented threshold:
// a tick late by a whole interval (one tick missed outright) is ordinary
// enough to judge silence by; any later than that is a stall.
func TestWatchdog_StallThresholdIsOneMissedTick(t *testing.T) {
	r := newStallRig(t)
	at := r.clock.now()
	r.tick(at)

	at = at.Add(2 * HeartbeatInterval) // the tick in between was missed
	r.tick(at)
	assert.Empty(t, readAuditKind(t, r.c, "watchdog_stall"), "late by exactly one interval is not a stall")

	r.tick(at.Add(2*HeartbeatInterval + time.Millisecond))
	assert.Len(t, readAuditKind(t, r.c, "watchdog_stall"), 1, "late by more than one interval is a stall")
}
