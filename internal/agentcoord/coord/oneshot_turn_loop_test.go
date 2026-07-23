package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// oneShotSpawner is startRunSpawner's one-shot sibling: a migrated,
// resume-capable agent resolved to ResumeModeOneShot, backed by a scriptedChat
// that live-confirms the loadSession capability (resumable) so the turn loop
// actually tears the engine down at each boundary.
func oneShotSpawner(mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: "container", profiles: []string{"p1"},
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
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "carry on", nil, "")
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

	// Still no exited spam after the resumed turn also one-shot-ended.
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	assertNoMailKind(t, c, KindExited, 200*time.Millisecond)
}

// TestApproval_MidTurnWaitYieldsSlotToPeer proves the mid-turn approval
// slot-yield (Slice 4 / Fork 1's companion): a child blocked on a human
// approval releases its ceiling slot so a queued peer can execute, instead of
// starving it up to the (now finite) cap. Cap 1: child A parks on an approval
// mid-turn; child B, queued behind the cap, must run to completion WHILE A is
// still blocked — only possible if A yielded its slot.
func TestApproval_MidTurnWaitYieldsSlotToPeer(t *testing.T) {
	resetStrictness(t)
	var spawns int
	sp := planPresetSpawner(func() *scriptedChat {
		spawns++
		if spawns == 1 {
			return &scriptedChat{permission: commandExecRequest("perm-A")} // A parks on approval
		}
		return &scriptedChat{} // B just runs
	})
	c := newTestCoordinatorCap(t, sp, nil, 1) // cap 1: B can only run if A yields its slot

	a, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task A", "", "")
	require.NoError(t, err)

	// A relays its approval to the parent and — crucially — yields its slot
	// while parked.
	appr := recvKind(t, c, "approval_request", conformanceWait)
	require.Len(t, appr, 1, "A must relay its approval to the parent")
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateParked }, conformanceWait, 10*time.Millisecond,
		"A must be parked (slot yielded) while it waits on the approval")

	// B, queued behind the cap-1 ceiling, now executes to completion — proof
	// the slot was freed by A's approval wait.
	b, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task B", "", "")
	require.NoError(t, err)
	bRes := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task B") }, conformanceWait)
	require.NotEmpty(t, bRes, "B must run to completion while A is approval-blocked (A yielded its slot)")

	// A is still parked on its unanswered approval.
	assert.Equal(t, StateParked, rosterState(c, a.Harp), "A must still be parked on its approval")

	// Answer A: it reclaims a slot and finishes its turn.
	_, err = c.AgentSend(ownerIdentity(), a.Harp, "", "ok", decisionJSON(t, "DECISION_ACCEPT"), appr[0].ID)
	require.NoError(t, err)
	aRes := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task A") }, conformanceWait)
	require.NotEmpty(t, aRes, "A resumes and completes its turn once the approval is answered")
	_ = b
}

// countRuns returns how many run records the live fold currently holds.
func countRuns(c *Coordinator) int {
	n := 0
	c.runs.View(func() { n = len(c.runsF.runs) })
	return n
}

// TestOneShot_RetentionReapsEndedRunsKeepsResumeKey drives a one-shot child
// through many turns (each turn mints one ended run for the same harp) under a
// tiny retention tail, and proves the live fold does NOT grow one record per
// turn forever: old ended runs are reaped, while the harp's CURRENT run (its
// resume key) is always kept and resume keeps working.
func TestOneShot_RetentionReapsEndedRunsKeepsResumeKey(t *testing.T) {
	resetStrictness(t)
	sp := oneShotSpawner(func() *scriptedChat { return &scriptedChat{resumable: true} })
	c, err := New(Options{
		ProjectDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		Spawner:      sp,
		EndedRunTail: 2, // keep only the newest 2 ended runs across all harps
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "turn 0", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)

	const turns = 8
	for i := 1; i <= turns; i++ {
		_, err := c.AgentSend(ownerIdentity(), out.Harp, "", "turn", nil, "")
		require.NoError(t, err)
		awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
		require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp))
		cancel()
		// Each resumed turn one-shot-ends again.
		require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	}

	// The fold is BOUNDED — not turns+1 records. With tail=2 it holds the
	// current run + at most ~2 retained ended runs (a small reap-timing slack),
	// far below the 9 a no-retention build would accumulate.
	require.Eventually(t, func() bool { return countRuns(c) <= 4 }, conformanceWait, 10*time.Millisecond,
		"one-shot ended runs must be reaped, not accumulate one per turn")

	// The resume key still resolves off the harp's current run, and a further
	// resume still rides it — reaping never touched the live key.
	sid, ok := c.resumeKeyFor(out.Harp)
	require.True(t, ok, "the harp's resume key must survive reaping")
	assert.Equal(t, "native-sess-42", sid)

	before := sp.chatCount()
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "final", nil, "")
	require.NoError(t, err)
	awaitCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp))
	cancel()
	require.Equal(t, before+1, sp.chatCount(), "resume must still spawn a fresh engine after reaping")
	sc := sp.chat(before)
	require.Eventually(t, func() bool {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		return len(sc.requests) == 1
	}, conformanceWait, 10*time.Millisecond)
	sc.mu.Lock()
	assert.Equal(t, "native-sess-42", sc.requests[0].ResumeSessionID, "resume must still ride the captured key after reaping")
	sc.mu.Unlock()
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
		"worker": {perm: "bypass", runtime: "container", profiles: []string{"p1"},
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
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "again", nil, "")
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
