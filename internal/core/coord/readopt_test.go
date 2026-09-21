package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crashCoordinator models the coordinator PROCESS dying: the journals close
// holding exactly what they hold, the listeners drop, and NO terminal is
// synthesized for any run — the runners are separate processes (their own
// session leaders on the host; a container's foreground) and keep running.
// Close is the graceful path and kills children first; a crash does neither.
func crashCoordinator(c *Coordinator) {
	c.closeOnce.Do(func() {
		c.tracked.Seal()
		c.closePartial() // journals and the owner lock go FIRST: nothing lands after this
		c.cancel()
		if t := c.takeTransport(); t != nil {
			t.Close()
		}
	})
}

// newTestCoordinatorOver serves a coordinator over a FIXED state dir with the
// given spawner — the restart half of an adoption test.
func newTestCoordinatorOver(t *testing.T, stateDir string, sp Spawner) *Coordinator {
	t.Helper()
	teeHome(t)
	c, err := New(Options{
		ProjectDir: stateDir,
		StateDir:   stateDir,
		Spawner:    sp,
		OwnerHarp:  ownerIdentity().Harp,
		Reporter:   termSink(),
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)
	return c
}

// TestReadopt_ARestartedCoordinatorReadoptsALiveRunner: the coordinator dies
// with a child parked on a live runner; the runner outlives it and redials
// the recorded endpoint. The restarted coordinator reads the journal and the
// session record, gives the run its runner-loss grace, and RE-ADOPTS the
// runner when its Hello names the run: the run is live again (not orphaned),
// mail reaches the SAME runner, and the run's cell ownership — the
// credential replicator the dead process held — is re-acquired through the
// spawner so the run's end can release it.
func TestReadopt_ARestartedCoordinatorReadoptsALiveRunner(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{} })
	t.Cleanup(func() {
		for i := 0; i < sp.spawnCount(); i++ {
			sp.killEngine(i)
		}
	})

	first := newTestCoordinatorOver(t, stateDir, sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(first, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	res1 := recvWhere(t, first, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task one") }, conformanceWait)
	require.NotEmpty(t, res1)

	crashCoordinator(first)

	second := newTestCoordinatorOver(t, stateDir, sp)
	// The endpoint is back: the runner is told to redial NOW rather than at
	// the end of its backoff (a dial that landed before Serve costs another
	// runnerHooks.HomeRedialBackoff, and two of them outrun the window under load). The
	// Hello then lands on the re-bound endpoint and names the run.
	sp.engineHome(0).Redial()
	require.Eventually(t, func() bool { return second.runnerConnected(out.RunID) }, conformanceWait, 10*time.Millisecond,
		"the live runner must re-Hello the restarted coordinator")
	assert.NotEqual(t, StateEnded, rosterState(second, out.Harp), "a run whose runner re-Hello'd is re-adopted, not orphaned")
	assert.Equal(t, "", runCause(second, out.RunID))
	assert.Equal(t, []string{out.Harp}, sp.adopted(), "re-adoption gives the run its cell owner back (the replicator's release)")

	// Mail reaches the SAME runner: no new spawn, the same run id.
	_, err = second.AgentSend(ownerIdentity(), out.Harp, KindMessage, "still there", nil, "")
	require.NoError(t, err)
	res2 := recvWhere(t, second, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "still there") }, conformanceWait)
	require.NotEmpty(t, res2, "turn 2 must reach the re-adopted runner")
	assert.Equal(t, 1, sp.spawnCount())
	assert.Equal(t, out.RunID, currentRunID(second, out.Harp))

	// The run's end releases what re-adoption acquired.
	second.terminateRun(out.RunID, CauseStopped, "test")
	require.Eventually(t, func() bool { return len(sp.adoptReleased()) == 1 }, conformanceWait, 10*time.Millisecond,
		"ending a re-adopted run releases the ownership re-adoption took")
}

// TestReadopt_ARunnerThatNeverReturns_IsRunnerLoss: the grace window is
// bounded. A run whose runner never re-Hellos after the restart ends as
// runner loss — for a HOST run too, which is no longer terminated on sight
// (its runner is its own session leader and may well be alive).
func TestReadopt_ARunnerThatNeverReturns_IsRunnerLoss(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{} })

	first := newTestCoordinatorOver(t, stateDir, sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(first, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	crashCoordinator(first)
	sp.killEngine(0) // the runner died with the host

	second := newTestCoordinatorOver(t, stateDir, sp)
	assert.NotEqual(t, StateEnded, rosterState(second, out.Harp), "adopt gives a host run the same grace a container run gets")
	second.expireRunnerGrace()
	require.Eventually(t, func() bool { return rosterState(second, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, CauseRunnerLoss, runCause(second, out.RunID))
}
