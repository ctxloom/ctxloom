package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end conformance for the MIGRATED (StartRun) spawn path — the C1
// cutover, hermetic: a real coordinator (live gRPC listeners, durable
// stores), a real runner half (Home + EngineHost dialed in by the fake
// spawner's StartEngine), and a scripted StructuredChat engine. No go-plugin,
// no containers — which is exactly the point: the delegated child's engine
// control rides StartRun; go-plugin's Chat is never dialed.

// startRunSpawner builds a fakeSpawner with one migrated agent named
// "worker".
func startRunSpawner(mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: "container", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.nextChat = mk
	return sp
}

// TestStartRun_EchoRoundTrip pins the spawn half of acceptance C1: agent_run
// on a migrated agent spawns the engine over StartRun (briefing + composed
// context as the first turn, model + permission through the HarnessSpec),
// the engine's native session id lands in the run journal (the resume
// handle), and the roster reaches idle at the turn boundary.
func TestStartRun_EchoRoundTrip(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing")
	require.NoError(t, err)

	// The engine received the briefing with the composed context leading it
	// (the leadContextIn contract, performed once coordinator-side).
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond, "the StartRun path must deliver the briefing as the first turn")
	first := sp.chat(0).recordedTexts()[0]
	assert.True(t, strings.HasPrefix(first, "FRAG-ONE\n\n"), "the composed context leads the first turn: %q", first)
	assert.Contains(t, first, "do the thing")

	// The chat request the engine saw carries the decoded HarnessSpec.
	sc := sp.chat(0)
	sc.mu.Lock()
	req := sc.requests[0]
	sc.mu.Unlock()
	assert.Equal(t, "test-model", req.Model, "the resolved model rides HarnessSpec.model")
	assert.Empty(t, req.ResumeSessionID, "a fresh spawn resumes nothing")

	// The native session id was journaled (via the harness_session event).
	require.Eventually(t, func() bool {
		sid := ""
		c.runs.View(func() {
			if r := c.runsF.run(out.RunID); r != nil {
				sid = r.HarnessSessionID
			}
		})
		return sid == "native-sess-42"
	}, conformanceWait, 10*time.Millisecond, "the engine's native session id must reach the run journal")

	// Turn boundary → idle on the roster (turn_idle folded).
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	// Journal proof: the interaction journal records start_run for this
	// run, and NO legacy chat launch happened (no fakeEngine was built).
	assert.Zero(t, sp.spawnCount(), "the legacy go-plugin Chat launch path must not fire for a migrated child")
	assert.Equal(t, 1, sp.chatCount())

	// The item events journaled (group-fsync path): the turn's message and
	// tool items are COUNTED in the items fold — the C1 journaling decision
	// (deltas counted, text never materialized).
	require.Eventually(t, func() bool {
		var counts map[string]int
		c.items.View(func() { counts = c.itemsF.countsFor(out.RunID) })
		return counts["run_started"] == 1 && counts["message_completed"] >= 2 &&
			counts["tool_call_completed"] == 1 && counts["message_delta"] >= 2
	}, conformanceWait, 10*time.Millisecond, "plane-1 items must journal (counted) for a migrated run")
}

// TestStartRun_SendToIdleChildStartsTurn pins acceptance C1's turn delivery:
// a parent send to an IDLE migrated child is pushed down the RunChannel and
// becomes a NEW TURN on the engine (framed with sender + kind), and its
// consumption fact advances the durable mailbox cursor.
func TestStartRun_SendToIdleChildStartsTurn(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.AgentSend(ownerIdentity(), out.Harp, "task", "second task", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 2
	}, conformanceWait, 10*time.Millisecond, "the send must start a new turn on the idle child")
	turn := sp.chat(0).recordedTexts()[1]
	assert.Contains(t, turn, "second task")
	assert.Contains(t, turn, "kind=task", "the kind survives as frame text (manly-grant (6))")
	assert.Contains(t, turn, "coordinator-harp", "the sender survives as frame text")

	// The consumption fact advanced the cursor: nothing left to deliver.
	require.Eventually(t, func() bool { return c.pendingCount(out.Harp) == 0 }, conformanceWait, 10*time.Millisecond,
		"the turn delivery must emit mail_consumed (durable cursor advance)")

	// And the child executed + returned to idle across that turn.
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
}

// TestStartRun_KillMidRunSynthesizesLossAndQueueAdvances pins acceptance C1's
// loss path on the migrated lifecycle: an externally killed engine (context
// torn down, nothing clean sent — the docker-stop shape) is noticed via
// RUNNER LOSS only (no chat-close exists for migrated children), the
// synthesized exit notice lands in the parent's mailbox, and the queued next
// child starts (the slot freed).
func TestStartRun_KillMidRunSynthesizesLossAndQueueAdvances(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task two")
	require.NoError(t, err)
	require.True(t, second.Queued, "the D4 cap queues the second child")

	sp.killEngine(0) // docker-stop: no RunExited, no clean teardown

	require.Eventually(t, func() bool { return rosterState(c, first.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"runner loss must synthesize the migrated child's terminal")
	require.Eventually(t, func() bool { return sp.chatCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"the queue must advance once the slot frees")

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	found := false
	for _, m := range msgs {
		if m.Kind == KindExited && m.From == first.Harp {
			found = true
		}
	}
	assert.True(t, found, "the parent's mailbox gets the synthesized exit notice")
}

// TestStartRun_ResumeUsesJournaledHarnessSessionID pins acceptance C1's
// resume: after a child's run ends, a parent send resumes the harp as a
// FRESH run whose StartRun carries the JOURNALED harness-native session id —
// the engine continues its own recorded session (no transcript re-priming).
func TestStartRun_ResumeUsesJournaledHarnessSessionID(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one")
	require.NoError(t, err)
	// Wait for the session id to journal BEFORE killing (the acceptance's
	// premise: the resume handle must already be durable).
	require.Eventually(t, func() bool {
		sid := ""
		c.runs.View(func() {
			if r := c.runsF.run(out.RunID); r != nil {
				sid = r.HarnessSessionID
			}
		})
		return sid == "native-sess-42"
	}, conformanceWait, 10*time.Millisecond)

	sp.killEngine(0)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)

	// A later send resumes the harp as a fresh run...
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "carry on", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sp.chatCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"the send to an ended harp must respawn it")
	// ...whose StartRun carried the journaled resume handle.
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
	assert.Equal(t, "native-sess-42", resumed, "resume must ride the journaled harness_session_id")

	// And the queued message arrives as the resumed engine's next turn.
	require.Eventually(t, func() bool {
		return len(sp.chat(1).recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Contains(t, sp.chat(1).recordedTexts()[0], "carry on")
}
