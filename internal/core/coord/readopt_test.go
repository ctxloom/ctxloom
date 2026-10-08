package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crashCoordinator models the coordinator PROCESS dying: the journals close
// holding exactly what they hold, the listeners drop, and NO terminal is
// synthesized for any run — the runners are separate processes (their own
// session leaders on the host; a container's foreground) and keep running.
// Close is the graceful path and kills children first; a crash does neither.
//
// A dying process stops everything AT ONCE, and no single order of the two
// teardowns models that. Journals first leaves a live transport serving frames
// against a closed journal — answered and acked as if recorded. Transport
// first leaves live journals recording the process's REACTIONS to its own cut:
// a pause in flight fails and drops its run from a hold, a runner stream's end
// declares runner loss. So it stops in three steps:
//
//  1. every journal write STALLS (stallJournals) — the process has stopped:
//     a frame being handled is neither recorded nor answered;
//  2. the streams are cut, from the coordinator's side, which no more frames
//     reach: the runner registrations are forgotten first, as a dead process
//     runs no stream teardown (DetachRunner records no runner loss);
//  3. the stalled writes, and every write after them, then FAIL — nothing the
//     cut provokes is recorded.
func crashCoordinator(c *Coordinator) {
	c.closeOnce.Do(func() {
		c.tracked.Seal()
		c.streams.Seal()
		fail := stallJournals(c.runs, c.items, c.auditJ)
		c.mu.Lock()
		var cut []context.CancelFunc
		for _, rs := range c.runners {
			cut = append(cut, rs.cancel)
		}
		for _, ch := range c.chans {
			cut = append(cut, ch.cancel)
		}
		clear(c.runners)
		c.mu.Unlock()
		for _, cancel := range cut {
			cancel()
		}
		fail()
		if t := c.takeTransport(); t != nil {
			t.Close()
		}
		c.closePartial() // journals and the owner lock: nothing lands after this
		c.cancel()
	})
}

// stalledWrites is a journal file whose appends block until stall closes and
// then fail: the dead process's view of its own journal.
type stalledWrites struct {
	afero.File
	stall <-chan struct{}
}

func (w stalledWrites) Write([]byte) (int, error) {
	<-w.stall
	return 0, errors.New("the coordinator process has died")
}

// stallJournals makes every append to stores block, returning the func that
// turns them (and every later one) into failures.
func stallJournals(stores ...*Store) (fail func()) {
	stall := make(chan struct{})
	for _, s := range stores {
		s.mu.Lock()
		s.f = stalledWrites{File: s.f, stall: stall}
		s.mu.Unlock()
	}
	return func() { close(stall) }
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
// spawner so the run's end can release it. The grace window closing after
// that re-Hello leaves the run alone.
func TestReadopt_ARestartedCoordinatorReadoptsALiveRunner(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })

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
	// redial backoff, and two of them outrun the window under load). The
	// Hello then lands on the re-bound endpoint and names the run.
	sp.engineHome(0).Redial()
	require.Eventually(t, func() bool { return second.runnerConnected(out.RunID) }, conformanceWait, 10*time.Millisecond,
		"the live runner must re-Hello the restarted coordinator")
	assert.NotEqual(t, StateEnded, rosterState(second, out.Harp), "a run whose runner re-Hello'd is re-adopted, not orphaned")
	assert.Equal(t, "", runCause(second, out.RunID))

	// The grace window still closes after the re-Hello; closing it must not
	// end a run whose runner came back.
	second.expireRunnerGrace()
	assert.NotEqual(t, StateEnded, rosterState(second, out.Harp), "an expired grace spares a run whose runner re-Hello'd")
	assert.Equal(t, "", runCause(second, out.RunID))

	// Mail reaches the SAME runner: no new spawn, the same run id.
	_, err = second.AgentSend(ownerIdentity(), out.Harp, KindMessage, "still there", nil, "")
	require.NoError(t, err)
	res2 := recvWhere(t, second, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "still there") }, conformanceWait)
	require.NotEmpty(t, res2, "turn 2 must reach the re-adopted runner")
	assert.Equal(t, 1, sp.spawnCount())
	assert.Equal(t, out.RunID, currentRunID(second, out.Harp))

}

// TestReadopt_ARunnerThatNeverReturns_IsRunnerLoss: the grace window is
// bounded. A run whose runner never re-Hellos after the restart ends as
// runner loss — for a HOST run too, which is no longer terminated on sight
// (its runner is its own session leader and may well be alive).
func TestReadopt_ARunnerThatNeverReturns_IsRunnerLoss(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })

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

// TestReadopt_ARunRefusedAtSealEndsAsRunnerLossUnlaunched: the run an
// agent_run left enqueued when its launch was refused at seal
// (TestAgentRun_RefusedOnceSealedLeavesTheRunEnqueued) has no runner and
// never had one. The next coordinator's adopt gives it the same grace as any
// unended run; when the grace runs out it ends as runner loss, and nothing
// launches it on the way.
func TestReadopt_ARunRefusedAtSealEndsAsRunnerLossUnlaunched(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })

	first := newTestCoordinatorOver(t, stateDir, sp)
	first.tracked.Seal()
	_, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.ErrorIs(t, err, ErrGroupSealed)
	require.Len(t, sp.assignedSessions(), 1)
	harp := sp.assignedSessions()[0]
	rec := first.currentRunRecord(harp)
	require.NotNil(t, rec, "the refused run was never enqueued")
	crashCoordinator(first)

	second := newTestCoordinatorOver(t, stateDir, sp)
	assert.Equal(t, StateQueued, second.runState(rec.RunID), "adopt must carry the enqueued run into its grace, not end or launch it")
	second.expireRunnerGrace()
	assert.Equal(t, StateEnded, second.runState(rec.RunID))
	assert.Equal(t, CauseRunnerLoss, runCause(second, rec.RunID))
	assert.Zero(t, sp.spawnCount(), "a run refused at seal was launched")
}

// TestReadopt_ARestartSlowerThanTheGraceStillReadoptsTheRunner: a coordinator
// that is DOWN ends nothing, so a restart that takes longer than the
// coordinator's own runner-loss grace (runnerLossTimeout) must still find its
// runners alive and re-adopt them. The runner's owner-loss window is its own
// (runner.DefaultOwnerLossWindow), deliberately longer than any grace; tying
// it to runnerLossTimeout killed every live child of a coordinator that took
// over 20s to come back.
func TestReadopt_ARestartSlowerThanTheGraceStillReadoptsTheRunner(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })

	first := newTestCoordinatorOver(t, stateDir, sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(first, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	crashCoordinator(first)
	// The coordinator stays down longer than its own grace. Nothing here is a
	// race to win: the premise IS the elapsed time.
	time.Sleep(runnerLossTimeout + 10*time.Second)

	// No Redial kick: in production nothing tells the runner its coordinator
	// is back, so its own loops — and its conns' reconnect backoff — must
	// find it within a redial or two.
	second := newTestCoordinatorOver(t, stateDir, sp)
	require.Eventually(t, func() bool { return second.runnerConnected(out.RunID) }, conformanceWait, 10*time.Millisecond,
		"a runner must outlive a coordinator restart slower than the coordinator's own grace")
	assert.NotEqual(t, StateEnded, rosterState(second, out.Harp))

	_, err = second.AgentSend(ownerIdentity(), out.Harp, KindMessage, "still there", nil, "")
	require.NoError(t, err)
	res := recvWhere(t, second, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "still there") }, conformanceWait)
	require.NotEmpty(t, res, "the re-adopted runner takes the next turn")
	assert.Equal(t, 1, sp.spawnCount())
}

// TestReadopt_ShutdownDrainDoesNotWaitOnAnAdoptedRunWithNoRunner: a run the
// journal says was mid-turn when its coordinator died, and whose runner has
// not dialed back, has no process this coordinator can ask to exit — there
// is nothing for the drain to wait on. A session that ends inside the
// adoption grace used to sit in its exit for the whole grace window
// (runnerLossTimeout) on such a run. The drain settles at once and leaves the
// run exactly as adoption found it: its grace belongs to whichever
// coordinator hosts the project next, which can still re-adopt a runner that
// was merely slow to redial.
func TestReadopt_ShutdownDrainDoesNotWaitOnAnAdoptedRunWithNoRunner(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	gate := make(chan struct{}) // never closed: the turn is in flight when the coordinator dies
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{Gate: gate} })

	first := newTestCoordinatorOver(t, stateDir, sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats) == 1 && len(sp.chats[0].RecordedTexts()) == 1
	}, conformanceWait, 5*time.Millisecond, "the turn must reach the engine: the run dies mid-turn, not mid-launch")
	crashCoordinator(first)
	sp.killEngine(0) // the runner died with the host

	second := newTestCoordinatorOver(t, stateDir, sp)
	second.drainBound = time.Minute
	require.Equal(t, StateExecuting, rosterState(second, out.Harp), "precondition: adoption holds the run open for its grace")

	started := time.Now()
	outcome := awaitDrain(t, second.BeginDrain())
	assert.Less(t, time.Since(started), runnerLossTimeout/4, "the drain must not wait out the adoption grace")
	assert.NotContains(t, outcome.Interrupted, out.Harp, "an adopted run with no runner is not forced")
	assert.NotEqual(t, StateEnded, rosterState(second, out.Harp), "the drain leaves the run for the next coordinator's grace")
}
