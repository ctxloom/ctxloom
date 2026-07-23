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
