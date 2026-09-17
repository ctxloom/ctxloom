package coord

import (
	"context"
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
	require.Less(t, i, len(sp.engines), "engine %d was never launched", i)
	e := sp.engines[i]
	sp.mu.Unlock()
	select {
	case <-e.releasedCh():
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
// with no run_id, every child ended within the bound, each NAMED with its
// outcome, the roster showing none live afterwards, and every engine's Close
// fired (the container-release seam).
func TestStopChildren_StopsEveryLiveChildWithinTheBoundAndNamesEach(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the running child never yields
	var launches int
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat {
			launches++
			if launches == 1 {
				return &scriptedChat{turnGate: gate}
			}
			return &scriptedChat{}
		})
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 300 * time.Millisecond

	running := spawnGatedChild(t, sp, c)
	idle := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, idle) == StateIdle }, conformanceWait, 5*time.Millisecond)
	require.Equal(t, StateExecuting, rosterState(c, running))

	started := time.Now()
	out, err := c.StopChildren(context.Background(), ownerIdentity(), "sweeping idle workers before the next fan-out")
	require.NoError(t, err)
	elapsed := time.Since(started)

	assert.GreaterOrEqual(t, elapsed, c.drainBound, "a running turn gets the whole bound before it is forced")
	assert.Less(t, elapsed, drainWait, "the sweep settles at the bound, not on the turn")

	require.Len(t, out, 2, "the result names EVERY child that was live, not a count: %+v", out)
	by := outcomeByHarp(out)
	assert.Equal(t, StopOutcomeStopped, by[idle].Outcome, "a child between turns ends at once")
	assert.Equal(t, StopOutcomeInterrupted, by[running].Outcome, "a child still running at the bound is forced and says so")
	for _, sc := range out {
		assert.NotEmpty(t, sc.RunID, "each entry carries the run that was ended: %+v", sc)
		assert.Equal(t, "worker", sc.Agent)
		assert.NotEmpty(t, sc.Detail)
	}

	for _, e := range c.Roster() {
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
	forced := readAuditKind(t, c, "drain_force")
	require.Len(t, forced, 1, "the force at the bound is on the record")
	assert.Equal(t, running, forced[0].Detail["harp"])
}

// TestStopChildren_InFlightTurnEndsAtItsBoundaryNotBefore: the bulk stop
// REQUESTS exit first. A turn shorter than the bound is not cut off — it
// reaches its boundary, its result is bridged, and the child ends THERE as
// stopped, not interrupted.
func TestStopChildren_InFlightTurnEndsAtItsBoundaryNotBefore(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute // far past the test: a sweep that waits for it fails

	harp := spawnGatedChild(t, sp, c)

	done := make(chan []StoppedChild, 1)
	go func() {
		out, err := c.StopChildren(context.Background(), ownerIdentity(), "done with this batch")
		require.NoError(t, err)
		done <- out
	}()
	select {
	case <-done:
		t.Fatal("the sweep settled while a turn was still in flight and the bound had not elapsed")
	case <-time.After(100 * time.Millisecond):
	}
	assert.Equal(t, StateExecuting, rosterState(c, harp), "the running turn is left alone until its boundary")

	close(gate) // the turn completes on its own

	var out []StoppedChild
	select {
	case out = <-done:
	case <-time.After(drainWait):
		t.Fatal("the sweep did not settle once the turn reached its boundary")
	}
	require.Len(t, out, 1)
	assert.Equal(t, StopOutcomeStopped, out[0].Outcome, "a turn that reached its boundary inside the bound was not interrupted")
	assert.Equal(t, StateEnded, rosterState(c, harp))
	assert.Equal(t, CauseStopped, currentRunCause(c, harp))
	results := recvKind(t, c, "result", conformanceWait)
	assert.NotEmpty(t, results, "the completed turn's result still reaches the parent")
	awaitRelease(t, sp, 0)
}

// TestStopChildren_ParkedChildIsEnded: a child parked in agent_recv is waiting
// on its parent — the very caller ending it. Unlike the coordinator's
// shutdown drain (which leaves a park alone as a wait on a human), the bulk
// stop ends it: leaving it would leave the roster live and its container up,
// which is exactly what the sweep exists to prevent.
func TestStopChildren_ParkedChildIsEnded(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the turn stays open under the park
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	harp := spawnGatedChild(t, sp, c)
	// Park it through the production path (a child waiting in agent_recv
	// yields its slot mid-turn and holds the turn open), with a long-poll
	// registered so the severance can be observed.
	var runID string
	c.runs.View(func() { runID = c.runsF.currentRun(harp).RunID })
	child := Identity{Harp: harp, RunID: runID, Depth: 1}
	severed := make(chan error, 1)
	go func() {
		_, rerr := c.AgentRecv(context.Background(), child, conformanceWait)
		severed <- rerr
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.polls[harp] != nil
	}, conformanceWait, 10*time.Millisecond)
	c.onRolePark(harp)
	require.Equal(t, StateParked, rosterState(c, harp), "precondition: the child is parked")

	started := time.Now()
	stopped, err := c.StopChildren(context.Background(), ownerIdentity(), "batch complete")
	require.NoError(t, err)
	assert.Less(t, time.Since(started), c.drainBound, "a parked child holds no turn to wait for")
	require.Len(t, stopped, 1)
	assert.Equal(t, StopOutcomeStopped, stopped[0].Outcome)
	assert.Contains(t, stopped[0].Detail, "while parked")
	assert.Equal(t, StateEnded, rosterState(c, harp))
	select {
	case rerr := <-severed:
		require.ErrorIs(t, rerr, ErrRevoked, "the parked poll is severed like any agent_stop's")
	case <-time.After(conformanceWait):
		t.Fatal("the parked poll was never severed")
	}
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
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	mine := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, mine) == StateIdle }, conformanceWait, 5*time.Millisecond)
	// A child of ANOTHER session under the same coordinator.
	other, err := c.AgentRun(context.Background(), Identity{Harp: "other-coordinator", Depth: 0}, "worker", "go", "", "")
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
		func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 200 * time.Millisecond

	harp := spawnGatedChild(t, sp, c)
	_, err := c.AgentSend(ownerIdentity(), harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	require.Positive(t, c.pendingCount(harp), "precondition: mail is queued when the child is swept")

	out, err := c.StopChildren(context.Background(), ownerIdentity(), "abandoning this line")
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, StopOutcomeInterrupted, out[0].Outcome)

	launches := func() int {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.engines)
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
// died of, not repainted as a stop.
func TestStopChildren_ChildDyingDuringSweepIsNotRelaunched(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

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
	require.Eventually(t, func() bool { return len(readAuditKind(t, c, "drain_request")) == 1 }, conformanceWait, 10*time.Millisecond,
		"the sweep must have requested the running child's exit before it dies")
	var runID string
	c.runs.View(func() { runID = c.runsF.currentRun(harp).RunID })
	c.terminateRun(runID, CauseRunnerExit, "engine crashed during the sweep")

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
		return len(sp.engines)
	}
	assert.Never(t, func() bool { return launches() > 1 }, 500*time.Millisecond, 10*time.Millisecond,
		"a child that dies during a sweep must NOT be relaunched")
	assert.Equal(t, CauseRunnerExit, currentRunCause(c, harp))
	assert.Positive(t, c.pendingCount(harp), "its mail is preserved for whoever runs next, not consumed")
}
