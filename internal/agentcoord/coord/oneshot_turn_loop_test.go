package coord

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// oneShotSpawner is startRunSpawner's one-shot sibling: a migrated,
// resume-capable agent resolved to ResumeModeOneShot, backed by a scriptedChat
// that live-confirms the loadSession capability (resumable) so the turn loop
// actually tears the engine down at each boundary.
func oneShotSpawner(mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"},
			viaStartRun: true, backend: "codex", oneshot: true},
	}, nil)
	sp.nextChat = mk
	return sp
}

// runCause reads a run's terminal cause off the fold ("" while live).
func runCause(c *Coordinator, runID string) string {
	cause := ""
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			cause = r.Cause
		}
	})
	return cause
}

// TestOneShot_TurnBoundaryTearsDownAndResumesByKey is Slice 4's real gate: a
// driving:oneshot child, live-confirmed resume-capable, runs turn 1; its engine
// process is torn down AT THE TURN BOUNDARY (a resumable terminal, NOT an
// agent_stop) with no external kill; a later agent_send RESUMES it by the
// captured native session id (session/load — not a cold start, not a lossy
// transcript replay); and the second turn's result is delivered. The
// ACCEPT_FOR_SESSION grant and the harp survive the boundary; the parent is
// NEVER spammed with a per-turn "exited" notice.
func TestOneShot_TurnBoundaryTearsDownAndResumesByKey(t *testing.T) {
	resetStrictness(t)
	sp := oneShotSpawner(func() *scriptedChat { return &scriptedChat{resumable: true} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)

	// A "don't ask again this session" grant, seeded on the harp: it must
	// outlive the one-shot boundary's terminal (only CauseStopped clears it).
	const kind = agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION
	c.cacheSessionAccept(out.Harp, kind)

	// Turn 1 runs, and the ENGINE ENDS ITSELF at the clean boundary — no
	// killEngine here, unlike the runner-loss resume test. The proof it was a
	// RESUMABLE teardown (not agent_stop): the run's terminal cause is
	// CauseOneShotBoundary while chatCount stays 1 (nothing resumed yet: the
	// mailbox is empty).
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"the one-shot engine must tear itself down at the turn boundary")
	assert.Equal(t, CauseOneShotBoundary, runCause(c, out.RunID),
		"the boundary teardown must be a resumable one-shot terminal, not a stop")
	assert.Equal(t, 1, sp.chatCount(), "nothing may resume while the mailbox is empty")

	// Turn 1's result was bridged to the parent (the child's answer, without
	// its model choosing to report), and NO exited notice was queued.
	res1 := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task one") }, conformanceWait)
	require.NotEmpty(t, res1, "turn 1's result must bridge to the parent")
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)

	// A later send RESUMES the harp by its captured native session key.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "carry on", nil, "")
	require.NoError(t, err)
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer awaitCancel()
	require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp), "the send to a one-shot-ended harp must resume it")
	require.Equal(t, 2, sp.chatCount(), "the resume must spawn a fresh engine")

	require.Eventually(t, func() bool {
		sc := sp.chat(1)
		if sc == nil {
			return false
		}
		sc.mu.Lock()
		defer sc.mu.Unlock()
		return len(sc.requests) == 1
	}, conformanceWait, 10*time.Millisecond)
	sc := sp.chat(1)
	sc.mu.Lock()
	resumed := sc.requests[0].ResumeSessionID
	sc.mu.Unlock()
	assert.Equal(t, "native-sess-42", resumed,
		"the resume must ride the captured session id (session/load), not a cold start or transcript replay")

	// The second turn ran and its result reached the parent.
	res2 := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "carry on") }, conformanceWait)
	require.NotEmpty(t, res2, "the resumed turn's result must be delivered")

	// The grant survived the whole turn boundary + resume (a new run_id) — the
	// harp-scoped ACCEPT_FOR_SESSION contract holds under one-shot.
	_, ok := c.sessionAccepted(out.Harp, kind)
	assert.True(t, ok, "the ACCEPT_FOR_SESSION grant must survive the one-shot boundary")

	// The resumed turn reaches a clean boundary (ended when its live confirm
	// landed in time — the common case — or idle if it raced and safely parked
	// warm), and either way the parent is NEVER spammed with a per-turn
	// "exited" notice.
	require.Eventually(t, func() bool {
		st := rosterState(c, out.Harp)
		return st == StateEnded || st == StateIdle
	}, conformanceWait, 10*time.Millisecond)
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)
}

// TestApproval_MidTurnWaitYieldsSlotToPeer proves the mid-turn approval
// slot-yield (Slice 4 / Fork 1's companion): a child blocked on a human
// approval releases its ceiling slot so a queued peer can execute, instead of
// starving it up to the (now finite) cap. Cap 1: child A parks on an approval
// mid-turn; child B, queued behind the cap, must EXECUTE (acquire the freed
// slot) WHILE A is still blocked — only possible if A yielded its slot.
//
// The slot-yield invariant is proven by B reaching StateExecuting (which
// happens in runChild the instant slots.acquire returns, BEFORE the engine
// turn) while A holds the only cap-1 slot parked — not by racing B's full
// engine-turn completion latency against a wall-clock deadline. That earlier
// shape flaked under full-suite load: B's *result* rides a whole
// in-process gRPC turn (Home dial + Chat + bridge) whose tail latency spikes
// under scheduler contention, so a 5s budget on the result occasionally timed
// out even though the slot had been yielded in microseconds. The mechanism
// itself is NOT load-sensitive: if the slot were not yielded (cap 1, A parked
// and only answered AFTER this assertion) B could never leave StateQueued at
// any budget — a hard deadlock, not a slow arrival — so this proof cannot be
// masked by load. B is held mid-turn by a turnGate so "B executing while A
// parked" is a stable, observable instant rather than a state B might race
// through before a poll sees it.
func TestApproval_MidTurnWaitYieldsSlotToPeer(t *testing.T) {
	resetStrictness(t)
	bGate := make(chan struct{})
	var spawns int
	// Explicit relay_to_role ladder, not the plan preset (relayLadderSpawner):
	// this test's subject is the SLOT-YIELD mechanism while ANY approval is
	// parked, not which preset selects relay_to_role — the plan preset no
	// longer relays a COMMAND_EXECUTION directly since marauding-hacksaw
	// (TestApproval_MidTurnWaitYieldsSlotToPeer's own park/yield assertions
	// hold identically for a surface_to_human park — see
	// TestApproval_SurfaceToHumanRoundTrip / surfaceApprovalToHuman's doc,
	// which documents the SAME onRolePark/onRoleUnpark discipline).
	sp := relayLadderSpawner(func() *scriptedChat {
		spawns++
		if spawns == 1 {
			return &scriptedChat{permission: commandExecRequest("perm-A")} // A parks on approval
		}
		return &scriptedChat{turnGate: bGate} // B: held mid-turn until released
	}, conformanceWait)
	c := newTestCoordinatorCap(t, sp, nil, 1) // cap 1: B can only run if A yields its slot

	a, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task A", "", "")
	require.NoError(t, err)

	// A relays its approval to the parent and — crucially — yields its slot
	// while parked.
	appr := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, appr, 1, "A must relay its approval to the parent")
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateParked }, conformanceWait, 10*time.Millisecond,
		"A must be parked (slot yielded) while it waits on the approval")

	// B, queued behind the cap-1 ceiling, acquires the FREED slot and reaches
	// StateExecuting — the direct proof A yielded. B is gated mid-turn, so it
	// holds this state for a stable observation. If the slot were NOT yielded
	// B would sit in StateQueued forever (A holds the only slot and is answered
	// below, AFTER this), so no wall-clock budget can mask a real starvation.
	b, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task B", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, b.Harp) == StateExecuting }, conformanceWait, 10*time.Millisecond,
		"B must reach StateExecuting (acquire the slot A yielded) while A is approval-blocked")

	// Both invariants hold at the same instant: B executing, A still parked on
	// its unanswered approval — B did not have to wait for A.
	assert.Equal(t, StateExecuting, rosterState(c, b.Harp), "B must be executing on the yielded slot")
	assert.Equal(t, StateParked, rosterState(c, a.Harp), "A must still be parked on its approval")

	// Release B: it runs its turn to completion (secondary confirmation; the
	// slot-yield claim is already proven above and does not hinge on this
	// engine-turn latency).
	close(bGate)
	bRes := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task B") }, conformanceWait)
	require.NotEmpty(t, bRes, "B completes its turn once released")

	// A is still parked on its unanswered approval after B has come and gone.
	assert.Equal(t, StateParked, rosterState(c, a.Harp), "A must still be parked on its approval")

	// Answer A: it reclaims a slot and finishes its turn.
	_, err = c.AgentSend(ownerIdentity(), a.Harp, "", "ok", decisionJSON(t, "DECISION_ACCEPT"), appr[0].ID)
	require.NoError(t, err)
	aRes := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task A") }, conformanceWait)
	require.NotEmpty(t, aRes, "A resumes and completes its turn once the approval is answered")
}

// countRuns returns how many run records the live fold currently holds.
func countRuns(c *Coordinator) int {
	n := 0
	c.runs.View(func() { n = len(c.runsF.runs) })
	return n
}

// TestReapEndedRuns_KeepsCurrentAndTail is the DETERMINISTIC unit test of the
// retention reap (Slice 4 / Fork 2.3): manufacturing several ended runs for
// one harp directly on the journal, reapEndedRuns must keep the harp's CURRENT
// run (its resume key) plus the newest EndedRunTail ended runs and drop the
// rest — the fold-level guarantee the one-shot loop relies on, tested without
// the engine timing the integration test is subject to.
func TestReapEndedRuns_KeepsCurrentAndTail(t *testing.T) {
	resetStrictness(t)
	c, err := New(Options{
		ProjectDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		Spawner:      newFakeSpawner(nil, nil),
		EndedRunTail: 2, // keep the newest 2 ended runs (beyond the current one)
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	const harp = "child-harp-X"
	base := time.Now().Add(-time.Minute) // recent: not max-age reaped
	// Six runs for one harp, oldest→newest; the last is the harp's current run
	// (byHarp points to the latest factRunEnqueued) and carries the resume key.
	runIDs := make([]string, 6)
	for i := range runIDs {
		id := fmt.Sprintf("run-x-%d", i)
		runIDs[i] = id
		at := base.Add(time.Duration(i) * time.Second)
		require.NoError(t, c.runs.Exec(func() ([]Fact, error) {
			return []Fact{
				factAt(factRunEnqueued, at, runEnqueued{RunID: id, Harp: harp, Agent: "worker", CredHash: id + "-cred", Depth: 1}),
				factAt(factRunHarness, at, runHarness{RunID: id, HarnessSessionID: "native-sess-42"}),
				factAt(factRunEnded, at, runEnded{RunID: id, Cause: CauseOneShotBoundary}),
			}, nil
		}))
	}
	require.Equal(t, 6, countRuns(c))

	c.reapEndedRuns()

	// Kept: the current run (run-x-5) + the newest 2 non-current ended runs
	// (run-x-4, run-x-3). Reaped: run-x-0..2.
	c.runs.View(func() {
		for _, keep := range []string{"run-x-5", "run-x-4", "run-x-3"} {
			assert.NotNil(t, c.runsF.run(keep), "%s must be retained", keep)
		}
		for _, gone := range []string{"run-x-0", "run-x-1", "run-x-2"} {
			assert.Nil(t, c.runsF.run(gone), "%s must be reaped", gone)
		}
	})
	assert.Equal(t, 3, countRuns(c), "current + tail(2) retained")

	// The harp's current run — and its resume key — survives the reap.
	sid, ok := c.resumeKeyFor(harp)
	require.True(t, ok, "the current run's resume key must survive reaping")
	assert.Equal(t, "native-sess-42", sid)

	// Idempotent: a second reap at the bounded floor changes nothing.
	c.reapEndedRuns()
	assert.Equal(t, 3, countRuns(c))
}

// TestRetention_BoundsFoldGrowthAcrossResumes is the integration half of the
// reap (Slice 4 / Fork 2.3): the wiring terminateRun → reapEndedRuns must keep
// the live fold bounded as one harp accumulates ended run after ended run. It
// uses a LEGACY in-process engine that EXITS after each turn (endAfterTurns:1)
// — so every mailbox delivery resumes the harp as a fresh, promptly-ended run,
// exactly the per-turn ended-run churn one-shot produces, but through the
// deterministic in-process legacy path rather than the migrated RunChannel
// (whose real engine round-trips this test must not race). With tail=1 the
// fold must never grow one record per resume.
func TestRetention_BoundsFoldGrowthAcrossResumes(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(
		map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{endAfterTurns: 1} }, // ends its run after each turn
	)
	c, err := New(Options{
		ProjectDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		Spawner:      sp,
		EndedRunTail: 1, // keep the current run + exactly one ended audit tail
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"the engine exits after its turn, ending the run")

	const resumes = 6
	for i := 1; i <= resumes; i++ {
		_, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "turn", nil, "")
		require.NoError(t, err)
		awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
		require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp))
		cancel()
		require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	}

	// Bounded: current + tail(1) = 2, far below the 7 ended runs a no-reap
	// build would accumulate. Eventually absorbs a tiny per-terminal reap lag.
	require.Eventually(t, func() bool { return countRuns(c) <= 3 }, conformanceWait, 10*time.Millisecond,
		"ended runs must be reaped as the harp resumes, not accumulate one per resume")
}

// TestOneShot_PersistentModeUnchanged proves the change is inert for a
// conversational (ResumeModePersistent) migrated child: its engine stays WARM
// across turns — the same process handles turn 2, no teardown, no resume — so
// the persistent model this release still ships is byte-for-byte untouched.
func TestOneShot_PersistentModeUnchanged(t *testing.T) {
	resetStrictness(t)
	// A resume-capable, live-confirmed engine but a PERSISTENT plan: the gate's
	// static half is false, so the boundary must NOT tear down.
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"},
			viaStartRun: true, backend: "codex"}, // oneshot:false
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{resumable: true} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	// The turn boundary parks the child idle (warm), NOT ended.
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond,
		"a persistent child parks idle at the boundary, engine warm")

	// A second turn is handled by the SAME engine process (no resume, no new
	// chat spawned).
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "again", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 2
	}, conformanceWait, 10*time.Millisecond, "the warm engine handles turn 2 itself")
	assert.Equal(t, 1, sp.chatCount(), "a persistent child must never spawn a second engine for a follow-up turn")
	// The second turn was a plain follow-up, not a resume-by-key.
	sc := sp.chat(0)
	sc.mu.Lock()
	require.Len(t, sc.requests, 1)
	assert.Empty(t, sc.requests[0].ResumeSessionID, "a persistent turn never rides a resume id")
	sc.mu.Unlock()
}
