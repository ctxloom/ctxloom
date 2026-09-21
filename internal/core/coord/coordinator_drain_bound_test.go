package coord

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ONE POLICY, TWO BOUNDS. Every coordinator wait is either BOUNDED or
// explicitly PARKED, and expiry is LOUD.
//
//   - A wait on a PROCESS (drain) is bounded at agent_recv's own maximum
//     wait, RecvWaitMax: exit is REQUESTED at drain start (no new
//     turn is handed out; a child ends at its next turn boundary) and FORCED
//     at the bound. A child that dies during drain is not relaunched — drain
//     is shutdown, not supervision.
//   - A wait on a HUMAN (a parked child) is a PARK, not a wait: unbounded, the
//     child holds its turn open, its session lock stays held, and the drain's
//     outcome lists it so the park cannot be forgotten.

// drainWait bounds how long a test waits for a drain that should complete
// well inside it; a drain that reaches it is a drain that did not settle.
const drainWait = 10 * time.Second

// awaitDrain waits for d to settle and returns its outcome.
func awaitDrain(t *testing.T, d *Drain) DrainOutcome {
	t.Helper()
	select {
	case <-d.Done():
	case <-time.After(drainWait):
		t.Fatal("the drain did not settle within the test's wait")
	}
	return d.Outcome()
}

// currentRunCause reads harp's CURRENT run's terminal cause off the folds
// ("" while it is still live) — runCause keyed by harp rather than run id.
func currentRunCause(c *Coordinator, harp string) string {
	runID := ""
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			runID = r.RunID
		}
	})
	return runCause(c, runID)
}

// spawnGatedChild spawns a legacy child whose first turn parks on gate: the
// turn stays IN FLIGHT until the test closes the gate (never, for a child that
// must not yield).
func spawnGatedChild(t *testing.T, sp *fakeSpawner, c *Coordinator) string {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(context.Background(), out.Harp))
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		if len(sp.chats) == 0 {
			return false
		}
		return len(sp.chats[0].RecordedTexts()) == 1
	}, conformanceWait, 5*time.Millisecond, "the first turn must reach the engine before the test acts")
	return out.Harp
}

// TestDrainBound_IsAgentRecvsMaxWait pins (d): the drain bound IS agent_recv's
// maximum wait — one symbol, read by both — never a second number.
func TestDrainBound_IsAgentRecvsMaxWait(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	assert.Equal(t, RecvWaitMax, c.drainBound,
		"the drain bound must be agent_recv's max wait, not a value of its own")
}

// TestBeginDrain_NeverYieldingChildIsForcedAtTheBoundAndNamed pins (a): a
// child that never reaches a turn boundary does not hold the drain open past
// the bound. It is asked first (the request is audited at drain start), forced
// at the bound, its terminal says it was interrupted, and the outcome names it.
func TestBeginDrain_NeverYieldingChildIsForcedAtTheBoundAndNamed(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the turn never yields
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 300 * time.Millisecond

	harp := spawnGatedChild(t, sp, c)
	require.Equal(t, StateExecuting, rosterState(c, harp), "precondition: the turn is in flight")

	started := time.Now()
	out := awaitDrain(t, c.BeginDrain())
	elapsed := time.Since(started)

	assert.GreaterOrEqual(t, elapsed, c.drainBound, "a running turn gets the whole bound before it is forced")
	assert.Less(t, elapsed, drainWait, "the drain must settle at the bound, not hang on the turn")
	assert.Equal(t, []string{harp}, out.Interrupted, "the outcome must NAME the interrupted child")
	assert.Empty(t, out.Exited)
	assert.Empty(t, out.Parked)
	assert.Equal(t, StateEnded, rosterState(c, harp))
	assert.Equal(t, CauseDrainInterrupted, currentRunCause(c, harp), "the terminal must say the child was interrupted, not merely stopped")

	requested := readAuditKind(t, c, "drain_request")
	forced := readAuditKind(t, c, "drain_force")
	require.Len(t, requested, 1, "exit is REQUESTED first, and the request is on the record")
	require.Len(t, forced, 1, "and FORCED at the bound, also on the record")
	assert.Equal(t, harp, requested[0].Detail["harp"])
	assert.Equal(t, harp, forced[0].Detail["harp"])
	assert.Equal(t, c.drainBound.String(), forced[0].Detail["bound"], "the force names the bound it enforced")

	// The parent learns of the interruption the way it learns of every
	// child death: a terminal notice naming the cause.
	notices := recvKind(t, c, KindExited, conformanceWait)
	require.NotEmpty(t, notices, "the parent's mailbox must carry the interruption")
	assert.Contains(t, notices[0].Body, CauseDrainInterrupted)
}

// TestBeginDrain_InFlightTurnEndsAtItsBoundaryNotBefore pins the REQUEST half:
// a turn shorter than the bound is not cut off. It reaches its boundary, its
// result is bridged to the parent as usual, and the child ends THERE — the
// request honoured — rather than parking idle for a next turn that will never
// be handed out.
func TestBeginDrain_InFlightTurnEndsAtItsBoundaryNotBefore(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute // far past the test: a drain that waits for it fails

	harp := spawnGatedChild(t, sp, c)

	d := c.BeginDrain()
	select {
	case <-d.Done():
		t.Fatal("the drain settled while a turn was still in flight and the bound had not elapsed")
	case <-time.After(100 * time.Millisecond):
	}
	assert.Equal(t, StateExecuting, rosterState(c, harp), "the running turn is left alone until its boundary")

	close(gate) // the turn completes on its own

	out := awaitDrain(t, d)
	assert.Equal(t, []string{harp}, out.Exited)
	assert.Empty(t, out.Interrupted, "a turn that reached its boundary inside the bound was not interrupted")
	assert.Equal(t, CauseDrained, currentRunCause(c, harp))
	results := recvKind(t, c, "result", conformanceWait)
	assert.NotEmpty(t, results, "the completed turn's result still reaches the parent")
}

// TestBeginDrain_IdleChildEndsImmediately: a child between turns has no
// process work to wait for. It ends at drain start, and the drain settles
// without touching the bound.
func TestBeginDrain_IdleChildEndsImmediately(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	idle := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, idle) == StateIdle }, conformanceWait, 5*time.Millisecond)

	started := time.Now()
	out := awaitDrain(t, c.BeginDrain())

	assert.Less(t, time.Since(started), c.drainBound)
	assert.Equal(t, []string{idle}, out.Exited)
	assert.Empty(t, out.Interrupted)
	assert.Equal(t, CauseDrained, currentRunCause(c, idle))
}

// TestBeginDrain_QueuedChildEndsImmediately: a child the concurrency cap has
// not yet admitted has not started, so there is nothing to request or wait
// for — it ends at drain start (never launched), while the executing child
// ahead of it gets the bound.
func TestBeginDrain_QueuedChildEndsImmediately(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })
	c := newTestCoordinatorCap(t, sp, nil, 1)
	c.drainBound = 300 * time.Millisecond

	running := spawnGatedChild(t, sp, c)
	queued, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "wait your turn", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, queued.Harp) == StateQueued }, conformanceWait, 5*time.Millisecond)

	out := awaitDrain(t, c.BeginDrain())

	assert.Equal(t, []string{queued.Harp}, out.Exited)
	assert.Equal(t, []string{running}, out.Interrupted)
	assert.Equal(t, CauseDrained, currentRunCause(c, queued.Harp))
	launches := func() int {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats)
	}
	assert.Equal(t, 1, launches(), "the queued child must never have been launched")
}

// TestBeginDrain_ChildDyingDuringDrainIsNotRelaunched pins (b): drain is
// shutdown, not supervision. A child that exits with mail still queued is
// ordinarily relaunched by terminateRun's leftover-mail tail; during drain it
// is not, and the drain settles with it listed as exited.
func TestBeginDrain_ChildDyingDuringDrainIsNotRelaunched(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	harp := spawnGatedChild(t, sp, c)
	// Mail queued mid-turn is what arms the relaunch when the child dies.
	_, err := c.AgentSend(ownerIdentity(), harp, KindMessage, "one more thing", nil, "")
	require.NoError(t, err)
	require.Positive(t, c.pendingCount(harp), "precondition: the child must have mail pending when it dies")

	d := c.BeginDrain()
	var runID string
	c.runs.View(func() { runID = c.runsF.currentRun(harp).RunID })
	// The child crashes mid-drain: the runner-exit terminal path, mail still
	// pending.
	c.terminateRun(runID, CauseRunnerExit, "engine crashed during drain")

	out := awaitDrain(t, d)
	assert.Equal(t, []string{harp}, out.Exited)

	launches := func() int {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats)
	}
	assert.Never(t, func() bool { return launches() > 1 }, 500*time.Millisecond, 10*time.Millisecond,
		"a child that dies during drain must NOT be relaunched")
	assert.Equal(t, StateEnded, rosterState(c, harp))
	assert.Equal(t, CauseRunnerExit, currentRunCause(c, harp), "the death is reported as what it was, not repainted")
	assert.Positive(t, c.pendingCount(harp), "its mail is preserved for whoever runs next, not consumed")
}

// TestTerminateRun_LeftoverMailRelaunchesAndDeliversIt is the positive pin
// the test above is the negative of: outside a drain, mail queued mid-turn
// that races the child's death (CauseRunnerExit — no drain, no sweep) is not
// stranded. terminateRun's tail (relaunchForLeftoverMail) relaunches the harp
// and THAT message is what the relaunched run receives as its first turn,
// leaving nothing pending. Deterministic: terminateRun is called directly,
// so the race is forced rather than raced.
func TestTerminateRun_LeftoverMailRelaunchesAndDeliversIt(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	harp := spawnGatedChild(t, sp, c)
	const leftover = "one more thing"
	_, err := c.AgentSend(ownerIdentity(), harp, KindMessage, leftover, nil, "")
	require.NoError(t, err)
	require.Positive(t, c.pendingCount(harp), "precondition: the child must have mail pending when it dies")

	var runID string
	c.runs.View(func() { runID = c.runsF.currentRun(harp).RunID })
	c.terminateRun(runID, CauseRunnerExit, "engine crashed mid-turn")

	require.Eventually(t, func() bool { return sp.chatCount() == 2 }, conformanceWait, 5*time.Millisecond,
		"a child that dies with mail pending must be relaunched exactly once")
	require.Eventually(t, func() bool { return len(sp.chat(1).RecordedTexts()) > 0 }, conformanceWait, 5*time.Millisecond,
		"the relaunched run must receive a first turn")
	texts := sp.chat(1).RecordedTexts()
	assert.Len(t, texts, 1, "the leftover mail is one turn, not several")
	assert.Contains(t, texts[0], leftover, "the relaunched run's first turn must carry the message that raced the death")
	// The consume-rename is the runner's ACCEPTANCE of the turn, and it
	// follows the engine seeing the text; the relaunch is judged on it.
	awaitSpoolCount(t, harp, spool.DirInConsumed, 1, "after the relaunched run took the leftover mail")
	assert.Zero(t, c.pendingCount(harp), "delivery consumes the mail; nothing is left queued behind the new run")
	assert.NotEqual(t, runID, currentRunID(c, harp), "the delivery rides a fresh run, not the dead one")
}

// TestBeginDrain_SendToEndedChildDoesNotResumeIt: an explicit agent_send to an
// ended child is ordinarily a resume (a fresh run). Under drain that is new
// work; the message is queued durably and the disposition says so rather than
// promising a resume that will not happen.
func TestBeginDrain_SendToEndedChildDoesNotResumeIt(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	harp := spawnOneChild(t, c)
	awaitDrain(t, c.BeginDrain())
	require.Equal(t, StateEnded, rosterState(c, harp), "precondition: the drain ended the idle child")

	disposition, err := c.AgentSend(ownerIdentity(), harp, KindMessage, "are you there", nil, "")
	require.NoError(t, err)
	assert.Contains(t, disposition, "draining")
	assert.NotContains(t, disposition, "resuming")

	launches := func() int {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats)
	}
	assert.Never(t, func() bool { return launches() > 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"an ended child must not be resumed by a send during drain")
	assert.Positive(t, c.pendingCount(harp), "the message waits in the mailbox")
}

// lockingSpawner holds a child's session lock the way the production spawner
// does (operations.AssignSessionHarp holds it; operations.EndSession releases
// it), so a drain test can observe the lock a parked child keeps.
type lockingSpawner struct{ *fakeSpawner }

func (s *lockingSpawner) AssignSession(projectDir, backend string) (string, error) {
	harp, err := s.fakeSpawner.AssignSession(projectDir, backend)
	if err != nil {
		return "", err
	}
	return harp, sessionlock.Hold(harp)
}

func (s *lockingSpawner) MarkSessionEnded(harp string) {
	s.fakeSpawner.MarkSessionEnded(harp)
	sessionlock.Release(harp)
}

// TestBeginDrain_ParkedChildIsNotWaitedOnAndKeepsItsSessionLock pins (c) and
// the PARK half of the policy: a child parked on a human is not a process
// wait. The drain settles without it, lists it, leaves its run and its turn
// open, and — well after the drain bound has elapsed — its session lock is
// still held, so a reaper that honours the lock cannot read it as dead.
func TestBeginDrain_ParkedChildIsNotWaitedOnAndKeepsItsSessionLock(t *testing.T) {
	resetStrictness(t)
	testsupport.Isolate(t)
	gate := make(chan struct{})
	sp := &lockingSpawner{fakeSpawner: newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })}
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 100 * time.Millisecond

	harp := spawnGatedChild(t, sp.fakeSpawner, c)
	t.Cleanup(func() { sessionlock.Release(harp) })
	// Park it through the production path: a child waiting in agent_recv
	// yields its slot mid-turn and holds the turn open.
	c.onRolePark(harp)
	require.Equal(t, StateParked, rosterState(c, harp), "precondition: the child is parked")
	require.Equal(t, sessionlock.Alive, sessionlock.Inspect(harp).Verdict, "precondition: the spawner holds the lock")

	started := time.Now()
	out := awaitDrain(t, c.BeginDrain())
	assert.Less(t, time.Since(started), c.drainBound, "a park is not waited on: the drain settles without spending the bound")
	assert.Equal(t, []string{harp}, out.Parked, "the outcome LISTS the parked child so the park cannot be forgotten")
	assert.Empty(t, out.Interrupted)
	assert.Empty(t, out.Exited)

	// Past the bound, and then some: still parked, still locked.
	assert.Never(t, func() bool { return rosterState(c, harp) != StateParked }, 3*c.drainBound, 10*time.Millisecond,
		"a parked child must not be forced when the drain bound elapses")
	assert.NotContains(t, sp.endedSessions(), harp, "its session was not ended")
	probe := sessionlock.Inspect(harp)
	assert.Equal(t, sessionlock.Alive, probe.Verdict, "its session lock is still held: %s", probe.Reason)
	assert.False(t, probe.Verdict.MayReclaim(), "a parked child's instance is not reclaimable")

	// Once the human answers, the park lifts and the drain policy applies:
	// the child ends at its boundary rather than taking another turn.
	c.onRoleUnpark(harp)
	close(gate)
	require.Eventually(t, func() bool { return rosterState(c, harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, CauseDrained, currentRunCause(c, harp))
}

// TestBeginDrain_RunEndedBeforeDrainBeganIsNotInTheOutcome pins the
// drainTracked skip end-to-end through BeginDrain, at the level DrainOutcome
// itself promises: a run that had already ended before BeginDrain was ever
// called is not "live when the drain began" and must not appear anywhere in
// the outcome — Exited included, since Exited is documented for children the
// drain itself watched end, not ones already dead on arrival.
func TestBeginDrain_RunEndedBeforeDrainBeganIsNotInTheOutcome(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	harp := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, harp) == StateIdle }, conformanceWait, 5*time.Millisecond)

	var runID string
	c.runs.View(func() { runID = c.runsF.currentRun(harp).RunID })
	c.terminateRun(runID, CauseStopped, "stopped well before any drain began")
	require.Equal(t, StateEnded, rosterState(c, harp), "precondition: the run ended before BeginDrain is ever called")

	out := awaitDrain(t, c.BeginDrain())
	assert.Empty(t, out.Exited, "a run already ended before the drain began was never live for it to track")
	assert.Empty(t, out.Interrupted)
	assert.Empty(t, out.Parked)
}

// TestBeginDrain_IsIdempotentAndReturnsTheSameDrain: a second BeginDrain does
// not start a second bounded wait; it returns the drain already in progress.
func TestBeginDrain_IsIdempotentAndReturnsTheSameDrain(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	first := c.BeginDrain()
	second := c.BeginDrain()
	assert.Same(t, first, second)
	awaitDrain(t, first)
	assert.Equal(t, DrainOutcome{}, first.Outcome(), "nothing was running: nothing to name")
}

// TestDrainBound_NoSecondLiteralExists pins the other half of (d) at the
// source: across the drain's reader (this package, which declares the verb's
// bound), the tool schema's clamp (mcpschema, which reads it by name) and
// agent_recv's readers (internal/adapters/mcp), the only duration expression
// equal to the bound is RecvWaitMax's own declaration. A second number,
// however it is spelled, is the drift this test exists to refuse.
func TestDrainBound_NoSecondLiteralExists(t *testing.T) {
	coordDir := packageDir(t)
	dirs := map[string]string{
		"coord":     coordDir,
		"mcpschema": filepath.Join(coordDir, "..", "..", "adapters", "coordgrpc", "mcpschema"),
		"mcp":       filepath.Join(coordDir, "..", "..", "adapters", "mcp"),
	}
	const declaring = "verbs.go"
	var seenDeclaration bool
	for name, dir := range dirs {
		for _, f := range nonTestGoFiles(t, dir) {
			for _, hit := range durationLiteralsEqualTo(t, f, RecvWaitMax) {
				if name == "coord" && filepath.Base(f) == declaring && hit == "RecvWaitMax" {
					seenDeclaration = true
					continue
				}
				t.Errorf("%s: a second spelling of the drain bound (%s) in %s: cite coord.RecvWaitMax instead", name, RecvWaitMax, f)
			}
		}
	}
	require.True(t, seenDeclaration, "the walker must find the declaration itself, or it is not finding anything")
	assert.Contains(t, referencingFiles(t, coordDir, "RecvWaitMax", false), "coordinator.go",
		"the drain bound must be read by name from its declaration")
}
