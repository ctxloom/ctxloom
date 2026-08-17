package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A finding claimed c.launches is an unbounded per-run projection that
// reapEndedRuns should bound like "every other" one. REFUTED, and this is the
// guard against the proposed remedy being applied:
//
//   - c.launches is keyed by HARP, not by run. reapEndedRuns bounds per-RUN
//     projections (runsFold.runs, queueFold.state, rosterFold.byRun); a harp
//     accumulates exactly one launchState however many runs it has, so one-shot's
//     run-per-turn does not grow it at all. c.byHarp is per-harp too, is never
//     deleted either, and holds a much larger struct — so c.launches is not the
//     outlier the finding describes.
//   - reapEndedRuns cannot even IDENTIFY a finished harp: it skips any run that
//     is its harp's CURRENT run, so every run it may reap belongs to a harp that
//     already has a NEWER one. Nothing in the folds ever says "this harp will
//     never run again".
//   - launchState.stopped deliberately OUTLIVES the run terminal. An agent_stop
//     can land on an already-ended run with a relaunch armed behind it; the run
//     record alone could not express "and do not bring it back". Deleting the
//     entry at a reap would drop that bit and reopen it.
//
// So: the bit survives a terminal AND a reap, and only an explicit new delivery
// lifts it.
func TestLaunchGate_StopSurvivesTheTerminalAndAReap(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)

	// Queue mail so the leftover-mail tail would genuinely want to relaunch:
	// without pending mail nextRelaunch is never consulted at all.
	_, _, err = c.queueMail(ownerIdentity().Harp, out.Harp, "task", "more work")
	require.NoError(t, err)

	c.cancelLaunch(out.Harp) // an explicit agent_stop
	c.terminateRun(out.RunID, CauseStopped, "stopped by the test")
	require.Eventually(t, func() bool { return c.runEnded(out.RunID) }, conformanceWait, 10*time.Millisecond)

	require.True(t, c.launchStopped(out.Harp), "the stop must survive the run's terminal")

	// The finding's own proposed bound, run for real. It must not clear the gate.
	c.reapEndedRuns()

	assert.True(t, c.launchStopped(out.Harp),
		"reaping ended RUN records must not drop the harp's stop bit — that bit is the only thing standing between an armed relaunch and the 2026-07-24 runaway")
	_, ok, exhausted := c.nextRelaunch(out.Harp)
	assert.False(t, ok, "a stopped harp must still refuse an automatic relaunch after a reap")
	assert.False(t, exhausted, "the refusal is the operator's stop being honoured, not budget exhaustion")

	c.mu.Lock()
	_, present := c.launches[out.Harp]
	c.mu.Unlock()
	assert.True(t, present, "the harp's launch state is per-HARP and outlives its runs by design")

	// Only an explicit new delivery lifts it — the documented way a stopped
	// child comes back.
	c.clearLaunchGate(out.Harp)
	assert.False(t, c.launchStopped(out.Harp))
	_, ok, _ = c.nextRelaunch(out.Harp)
	assert.True(t, ok, "after an explicit delivery the harp is relaunchable again")
}

// The per-harp/per-run distinction, pinned directly: many runs for one harp keep
// exactly one launchState, so the growth the finding describes is bounded by the
// number of distinct children a coordinator ever spawns — the same bound every
// other per-harp map in the package carries.
func TestLaunchGate_OneStatePerHarpNotPerRun(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	for range 5 {
		c.noteLaunchFailure("child-repeat")
		c.noteLaunchAttached("child-repeat")
	}
	c.mu.Lock()
	n := len(c.launches)
	c.mu.Unlock()
	assert.Equal(t, 1, n, "repeated launch cycles for one harp must not add entries")
}
