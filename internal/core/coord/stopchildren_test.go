package coord

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent_stop's BULK form: a coordinator that omits run_id (and names a
// reason) drains EVERY live child of its own, under the one drain bound, and
// gets back the per-child outcome. It exists because hang-open is the
// default — a child that files FINAL stays resumable rather than exiting — so
// idle children accumulate across a session, each holding a slot (and, for a
// container runtime, a live container), until the coordinator cannot spawn.
// The per-run form is the wrong tool for the sweep: a resumed child runs under
// a FRESH run_id, so any run_id captured at spawn is stale after a resume, and
// only the roster knows the current one.

// awaitRelease waits for the i-th fake engine's Close to fire — the seam the
// container teardown hangs off — and fails the test if it never does.
func awaitRelease(t *testing.T, sp *fakeSpawner, i int) {
	t.Helper()
	sp.mu.Lock()
	require.Less(t, i, len(sp.released), "engine %d was never launched", i)
	released := sp.released[i]
	sp.mu.Unlock()
	select {
	case <-released:
	case <-time.After(conformanceWait):
		t.Fatalf("engine %d's Close never fired: the child's process/container was not released", i)
	}
}

// outcomeByHarp indexes a StopChildren result by harp.
func outcomeByHarp(out []StoppedChild) map[string]StoppedChild {
	m := make(map[string]StoppedChild, len(out))
	for _, sc := range out {
		m[sc.Harp] = sc
	}
	return m
}

// TestStopChildren_StopsEveryLiveChildWithinTheBoundAndNamesEach is the
// settling condition in one: N live children in different states, one call
// with no run_id, every child ended within the bound — the running one
// interrupted rather than waited out — each NAMED with its outcome, the roster
// showing none live afterwards, and every engine's Close fired (the
// container-release seam).
func TestStopChildren_StopsEveryLiveChildWithinTheBoundAndNamesEach(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the running child never yields
	var launches int
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat {
			launches++
			if launches == 1 {
				return &scriptedChat{Gate: gate}
			}
			return &scriptedChat{}
		})
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute // far past the test: a sweep that waits for it fails

	running := spawnGatedChild(t, sp, c)
	idle := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, idle) == StateIdle }, conformanceWait, 5*time.Millisecond)
	require.Equal(t, StateExecuting, rosterState(c, running))

	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()
	out, err := c.StopChildren(ctx, ownerIdentity(), "sweeping idle workers before the next fan-out")
	require.NoError(t, err, "the running turn is interrupted, not waited out to the bound")

	require.Len(t, out, 2, "the result names EVERY child that was live, not a count: %+v", out)
	by := outcomeByHarp(out)
	assert.Equal(t, StopOutcomeStopped, by[idle].Outcome, "a child between turns ends at once")
	assert.Equal(t, StopOutcomeStopped, by[running].Outcome, "an interrupted turn ends inside the bound: nothing is forced")
	for _, sc := range out {
		assert.NotEmpty(t, sc.RunID, "each entry carries the run that was ended: %+v", sc)
		assert.Equal(t, "worker", sc.Agent)
		assert.NotEmpty(t, sc.Detail)
	}

	for _, e := range c.Roster(ownerIdentity()) {
		assert.Equal(t, StateEnded, e.State, "roster afterwards shows none live: %+v", e)
	}
	for _, harp := range []string{running, idle} {
		assert.Equal(t, CauseStopped, currentRunCause(c, harp), "a bulk stop is an agent_stop: its terminal cause is the same one")
	}
	awaitRelease(t, sp, 0)
	awaitRelease(t, sp, 1)

	// The stop is on the audit record per child, with the reason, exactly as
	// the per-run form records it.
	stops := readAuditKind(t, c, "agent_stop")
	require.Len(t, stops, 2)
	for _, s := range stops {
		assert.Equal(t, "sweeping idle workers before the next fan-out", s.Detail["reason"])
		assert.Equal(t, ownerIdentity().Harp, s.Actor)
	}
	assert.Empty(t, readAuditKind(t, c, "drain_force"), "nothing was forced")
}

// TestStopChildren_ParkedChildIsEnded: a parked child is waiting on a human,
// and the caller ending it is that human's session. Unlike the coordinator's
// shutdown drain (which leaves a park alone as a wait on a human), the bulk
// stop ends it: leaving it would leave the roster live and its container up,
// which is exactly what the sweep exists to prevent.
func TestStopChildren_ParkedChildIsEnded(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the turn stays open under the park
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	harp := spawnGatedChild(t, sp, c)
	// Park it mid-turn: the turn stays open under the park.
	c.mu.Lock()
	rt := c.byHarp[harp]
	c.mu.Unlock()
	require.NotNil(t, rt, "precondition: the spawned child must have a runtime attachment")
	c.setState(rt, StateParked)
	require.Equal(t, StateParked, rosterState(c, harp), "precondition: the child is parked")

	started := time.Now()
	stopped, err := c.StopChildren(context.Background(), ownerIdentity(), "batch complete")
	require.NoError(t, err)
	assert.Less(t, time.Since(started), c.drainBound, "a parked child holds no turn to wait for")
	require.Len(t, stopped, 1)
	assert.Equal(t, StopOutcomeStopped, stopped[0].Outcome)
	assert.Contains(t, stopped[0].Detail, "while parked")
	assert.Equal(t, StateEnded, rosterState(c, harp))
	awaitRelease(t, sp, 0)
}

// TestStopChildren_OnlyTheCallersOwnChildren: the sweep is scoped to the
// CALLER's children, never the whole coordinator — a coordinator-capable
// child sweeping its grandchildren must not take its siblings down, and the
// coordinator stays open for new work afterwards (this is a sweep, not the
// shutdown drain: admission is not closed).
func TestStopChildren_OnlyTheCallersOwnChildren(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	c.drainBound = time.Minute

	mine := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, mine) == StateIdle }, conformanceWait, 5*time.Millisecond)
	// A child of ANOTHER coordinator under the same process: the owner's
	// child, coordinating a grandchild of its own.
	var mineRunID string
	c.runs.View(func() { mineRunID = c.runsF.currentRun(mine).RunID })
	other, err := c.AgentRun(context.Background(), Identity{Harp: mine, RunID: mineRunID, Depth: 1}, "worker", "go", "", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(context.Background(), other.Harp))
	require.Eventually(t, func() bool { return rosterState(c, other.Harp) == StateIdle }, conformanceWait, 5*time.Millisecond)

	out, err := c.StopChildren(context.Background(), ownerIdentity(), "mine only")
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, mine, out[0].Harp)
	assert.Equal(t, StateEnded, rosterState(c, mine))
	assert.Equal(t, StateIdle, rosterState(c, other.Harp), "another session's child is not this caller's to stop")

	assert.False(t, c.Draining(), "a sweep is not the shutdown drain: admission stays open")
	again, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "next batch", "", "")
	require.NoError(t, err, "the coordinator can spawn again after the sweep — the reason the sweep exists")
	require.NoError(t, c.awaitChildUp(context.Background(), again.Harp))
}

// TestStopChildren_SweptChildStaysResumableButIsNotAutoRelaunched: hang-open
// is intentional — the swept child stays resumable by an explicit agent_send
// (a fresh run) — but mail it left queued must NOT bring it straight back,
// or the sweep would undo itself.
func TestStopChildren_SweptChildStaysResumableButIsNotAutoRelaunched(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 200 * time.Millisecond

	harp := spawnGatedChild(t, sp, c)
	_, err := c.AgentSend(ownerIdentity(), harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	require.Positive(t, c.pendingCount(harp), "precondition: mail is queued when the child is swept")

	out, err := c.StopChildren(context.Background(), ownerIdentity(), "abandoning this line")
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, StopOutcomeStopped, out[0].Outcome, "its turn was interrupted, not forced at the bound")

	launches := func() int {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats)
	}
	assert.Never(t, func() bool { return launches() > 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"a swept child must NOT be relaunched by its leftover mail")
	assert.Equal(t, StateEnded, rosterState(c, harp))

	// An explicit send is a fresh ask: the child resumes as a fresh run.
	close(gate)
	_, err = c.AgentSend(ownerIdentity(), harp, KindMessage, "actually, one more", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return launches() == 2 }, conformanceWait, 10*time.Millisecond,
		"an explicit agent_send still resumes a swept child")
}

// TestStopChildren_EmptyReasonRefused: the bulk form cannot be reached by
// accident. Omitting the run_id AND the reason is refused, naming what is
// missing, and nothing is stopped.
func TestStopChildren_EmptyReasonRefused(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	harp := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, harp) == StateIdle }, conformanceWait, 5*time.Millisecond)

	for _, reason := range []string{"", "   "} {
		out, err := c.StopChildren(context.Background(), ownerIdentity(), reason)
		require.ErrorIs(t, err, ErrStopReasonRequired)
		assert.Contains(t, err.Error(), "reason")
		assert.Nil(t, out)
	}
	assert.Equal(t, StateIdle, rosterState(c, harp), "a refused sweep stops nothing")
}

// TestStopChildren_NoLiveChildrenIsEmptyNotAnError: a sweep with nothing to
// sweep settles at once with an empty outcome.
func TestStopChildren_NoLiveChildrenIsEmptyNotAnError(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	started := time.Now()
	out, err := c.StopChildren(context.Background(), ownerIdentity(), "nothing to do")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Less(t, time.Since(started), c.drainBound)
}

// TestStopChildren_ChildDyingDuringSweepIsNotRelaunched: a child that exits
// on its own mid-sweep with mail still queued is ordinarily relaunched by
// terminateRun's leftover-mail tail. Under a sweep it is not — the sweep
// marked it stopped — and the result names it with the cause it actually
// died of, not repainted as a stop. The death is forced in the request hook:
// after the sweep marked it, before its runner is asked to stop it, which
// would otherwise end it first.
func TestStopChildren_ChildDyingDuringSweepIsNotRelaunched(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute
	c.drainRequestHook = func(runID string) {
		c.terminateRun(runID, CauseRunnerExit, "engine crashed during the sweep")
	}

	harp := spawnGatedChild(t, sp, c)
	_, err := c.AgentSend(ownerIdentity(), harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	require.Positive(t, c.pendingCount(harp), "precondition: the child must have mail pending when it dies")

	done := make(chan []StoppedChild, 1)
	go func() {
		out, err := c.StopChildren(context.Background(), ownerIdentity(), "winding down")
		require.NoError(t, err)
		done <- out
	}()
	var out []StoppedChild
	select {
	case out = <-done:
	case <-time.After(drainWait):
		t.Fatal("the sweep did not settle on the child's own death")
	}
	require.Len(t, out, 1)
	assert.Equal(t, StopOutcomeStopped, out[0].Outcome, "it ended without being forced")
	assert.Contains(t, out[0].Detail, CauseRunnerExit, "the death is reported as what it was, not repainted as a stop")

	launches := func() int {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats)
	}
	assert.Never(t, func() bool { return launches() > 1 }, 500*time.Millisecond, 10*time.Millisecond,
		"a child that dies during a sweep must NOT be relaunched")
	assert.Equal(t, CauseRunnerExit, currentRunCause(c, harp))
	assert.Positive(t, c.pendingCount(harp), "its mail is preserved for whoever runs next, not consumed")
}

// TestStopChildren_BoundaryRacingTheRequestStillEndsTheRun is the bulk-stop
// copy of TestFinalReport_BoundaryRacingTheRequestStillEndsTheRun: the sweep
// shares runDrain, so it shares the ordering that test forces — the drain reads
// the child as EXECUTING, the turn boundary lands before the exit mark, and the
// child parks idle with no later boundary to take the mark. A wait loop that
// counts that idle child as running holds the sweep for the whole drain bound,
// which is set far past the test here so that wait fails it.
func TestStopChildren_BoundaryRacingTheRequestStillEndsTheRun(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute
	c.drainRequestHook = func(runID string) {
		close(gate)
		deadline := time.Now().Add(conformanceWait)
		for c.runState(runID) != StateIdle && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return midTurn(c, sp, out.Harp) }, conformanceWait, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	stopped, err := c.StopChildren(ctx, ownerIdentity(), "done with this batch")
	require.NoError(t, err, "a child whose boundary beat the exit mark must be ended, not held idle for the drain bound")
	require.Len(t, stopped, 1)
	assert.Equal(t, StopOutcomeStopped, stopped[0].Outcome)
	assert.Equal(t, StateEnded, rosterState(c, out.Harp))
	assert.Equal(t, CauseStopped, runCause(c, out.RunID))
}

// TestStopChildren_MidStartRunIsAStopNotALaunchFailure is the bulk copy of
// TestAgentStop_MidStartRunIsAStopNotALaunchFailure: a stop that lands while
// the child's StartRun is still on the wire is a STOP, whichever form of
// agent_stop sent it. The sweep reads the child as executing and asks its
// runner to close the run; the StartRun held at the runner is released only
// once the runner has answered that stop, so the launch is then refused ("the
// run has ended") and fails while the stop is still pending — the losing
// interleaving, forced. The terminal must be the stop, carrying its reason.
func TestStopChildren_MidStartRunIsAStopNotALaunchFailure(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	sp.bindHold = make(chan struct{})
	sp.bindEntered = make(chan struct{}, 1)
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute
	var release sync.Once
	releaseHold := func() { release.Do(func() { close(sp.bindHold) }) }
	t.Cleanup(releaseHold)
	c.stopAnsweredHook = func(string) { releaseHold() }

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	select {
	case <-sp.bindEntered:
	case <-time.After(conformanceWait):
		t.Fatal("the child's StartRun never reached the runner")
	}
	require.Equal(t, StateExecuting, rosterState(c, out.Harp), "precondition: the sweep must find the child mid-launch")

	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()
	stopped, err := c.StopChildren(ctx, ownerIdentity(), "fan-out complete")
	require.NoError(t, err)
	require.Len(t, stopped, 1)
	assert.Equal(t, StopOutcomeStopped, stopped[0].Outcome)
	assert.Contains(t, stopped[0].Detail, "fan-out complete", "the reason reaches the child's terminal detail")
	assert.Equal(t, CauseStopped, runCause(c, out.RunID), "a launch the stop refused is the stop, not a launch failure")

	msgs, err := ownerMail(t, c, time.Second)
	require.NoError(t, err)
	var kinds []string
	for _, m := range msgs {
		if m.From == out.Harp {
			kinds = append(kinds, m.Kind)
		}
	}
	assert.Equal(t, []string{KindExited}, kinds, "the parent is told the child was stopped, never that it failed to launch: %+v", msgs)
}

// TestStopChildren_BoundaryTerminalDoesNotDrainItsOwnChannel: a swept child's
// turn boundary is a frame of its own run channel, handled on that channel's
// recv goroutine. The stop it ends the run with must not drain the channel:
// the drain would wait the whole window for a run_completed only that
// goroutine could read, and a sweep under a shorter bound would then report a
// clean stop as forced. The boundary is delivered directly, so the in-band
// path is taken every time rather than when it wins the race with the
// runner's own close.
func TestStopChildren_BoundaryTerminalDoesNotDrainItsOwnChannel(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	harp := spawnGatedChild(t, sp, c)
	runID := currentRunID(c, harp)

	drained := false
	c.drainHook = func(string) { drained = true }
	c.requestExit(runID, stopPolicy(ownerIdentity().Harp, "abandoning this line", 0))
	c.onTurnIdle(harp, runID)

	assert.Equal(t, StateEnded, rosterState(c, harp), "the marked child ends at its boundary")
	assert.Equal(t, CauseStopped, runCause(c, runID))
	assert.False(t, drained, "a terminal decided on the channel's own recv goroutine must not drain that channel")
}
