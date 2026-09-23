package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end conformance for the MIGRATED (StartRun) spawn path — the C1
// cutover, hermetic: a real coordinator (live gRPC listeners, durable
// stores), a real runner half (Home + EngineHost dialed in by the fake
// spawner's StartEngine), and a scripted structured-driver engine. No
// containers — the delegated child's engine control rides StartRun.

// startRunSpawner builds a fakeSpawner with one migrated agent named
// "worker".
func startRunSpawner(mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}},
	}, nil)
	sp.nextChat = mk
	return sp
}

// TestStartRun_EchoRoundTrip pins the spawn half of acceptance C1: agent_run
// on a migrated agent spawns the engine over StartRun (briefing + composed
// context as the first turn, model + permission through the HarnessSpec),
// the engine's native session id lands on the session entry (the resume
// handle), and the roster reaches idle at the turn boundary.
func TestStartRun_EchoRoundTrip(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// The engine received the briefing with the composed context leading it
	// (joined once coordinator-side).
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.RecordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond, "the StartRun path must deliver the briefing as the first turn")
	first := sp.chat(0).RecordedTexts()[0]
	assert.True(t, strings.HasPrefix(first, "FRAG-ONE\n\n"), "the composed context leads the first turn: %q", first)
	assert.Contains(t, first, "do the thing")

	// The turn the engine's driver ran carries the decoded launch.
	sc := sp.chat(0)
	assert.Equal(t, []string{""}, sc.RecordedKeys(), "a fresh spawn resumes nothing")

	// The native session id reached the session entry. "worker" runs in a
	// container (RuntimeRootless): the key it reports over the wire is the
	// only way it reaches the entry, since the child's own bind hook runs
	// inside the container.
	require.Eventually(t, func() bool { return sp.NativeSession(out.Harp) == "native-sess-42" },
		conformanceWait, 10*time.Millisecond, "the engine's native session id must reach the session entry")

	// Turn boundary → idle on the roster (turn_idle folded).
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	assert.Equal(t, 1, sp.chatCount())

	// The item events journaled (group-fsync path): the turn's message and
	// tool items are COUNTED in the items fold — the C1 journaling decision
	// (deltas counted, text never materialized).
	//
	// Polling the counts against a wall clock is what made this assertion
	// flaky: the fold is fed by flushItems, which APPENDS AND FSYNCS, and the
	// roster assertion above does not serialize against that — turn-idle folds
	// the roster (a DIFFERENT store) before handleAgentEvent flushes the
	// channel's item buffer, so "idle" is observable while items are still
	// draining. fsync latency is the unbounded term under whole-repo gate
	// load, which is how this expired at 5.03s in a merge gate.
	//
	// So wait for the DURABILITY EVENT rather than for a duration, then read
	// the counts ONCE. flushedSeq is advanced only by a flushItems whose
	// journal append+fsync returned, so a flushedSeq covering the runner's
	// last emitted seq means every fact this run produced is journaled AND
	// folded — after which the counts are a deterministic read that fails
	// immediately (with the actual counts) rather than after a timeout.
	awaitItemsDurable(t, c, sp, out.Harp)
	var counts map[string]int
	c.items.View(func() { counts = c.itemsF.countsFor(out.RunID) })
	assert.Equal(t, 1, counts["run_started"], "the migrated run must journal exactly one run_started (counts: %v)", counts)
	assert.GreaterOrEqual(t, counts["message_completed"], 2, "both of the turn's messages must journal a completion (counts: %v)", counts)
	assert.Equal(t, 1, counts["tool_call_completed"], "the turn's one tool call must journal its completion (counts: %v)", counts)
	assert.GreaterOrEqual(t, counts["message_delta"], 2, "deltas are COUNTED (never materialized), so both messages' text must show up as deltas (counts: %v)", counts)
}

// itemsDurableWait is the barrier budget for awaitItemsDurable, and it is
// deliberately far larger than an assertion budget because it is not an
// assertion: the thing being waited on is a journal append + fsync, whose
// latency has no bound the test can reason about when the whole repo's
// packages are fsyncing on the same disk. The inner check is an EVENT (the
// coordinator's own durable watermark), not a guess about how long a fold
// takes, so the budget only has to be longer than the worst drain — it never
// decides whether the assertion passes.
const itemsDurableWait = 30 * time.Second

// awaitItemsDurable blocks until every AgentEvent the run's runner has emitted
// has been journaled by the coordinator, i.e. until the run channel's
// flushedSeq (advanced only by a flushItems whose append+fsync returned)
// covers the runner Home's highest assigned seq. It is the synchronisation
// barrier that lets an item-fold assertion be a plain read.
func awaitItemsDurable(t *testing.T, c *Coordinator, sp *fakeSpawner, harp string) {
	t.Helper()
	require.Eventually(t, func() bool {
		h := sp.engineHome(0)
		if h == nil {
			return false
		}
		last := h.EmittedSeq()
		c.mu.Lock()
		ch := c.chans[harp]
		var flushed uint64
		if ch != nil {
			flushed = ch.flushedSeq
		}
		c.mu.Unlock()
		return ch != nil && last > 0 && flushed >= last
	}, itemsDurableWait, 10*time.Millisecond,
		"the coordinator's durable watermark never caught up with the events the runner emitted — the item journal is stalled, not merely slow")
}

// TestStartRun_BackendParity pins that a second backend label rides the
// IDENTICAL StartRun mechanics claude proved
// first — the coordinator/runner machinery (EngineHost, HarnessSpec codec,
// turn delivery, journaling) is backend-agnostic by construction (it only
// ever threads plan.Backend through as an opaque string, see
// runChildViaStartRun's Harness: rt.plan.Backend), so this is a hermetic,
// per-backend structural proof: each backend label reaches the StartRun
// path, completes a turn, and journals a
// RunStarted whose harness field records the SPECIFIC backend. The
// backend-SPECIFIC deltas (model delivery argv/env) are each backend's own
// concern and out of scope here. Note what this test is NOT: it drives a FAKE
// spawner and a scripted adapter, so no real engine process runs and no live
// multi-turn echo is exercised for any row — this is the stated hermetic
// substitute per the acceptance's own allowance.
//
// The per-backend proof is not cosmetic: the runner's EngineHost refuses a
// StartRun whose HarnessSpec.harness does not equal the name its own
// RunnerHello advertised (enginehost.go's A9-adjacent FailedPrecondition
// check, wired in fake_test's StartEngine from plan.Backend), so a backend
// whose name were dropped or coerced anywhere between Resolve and the wire
// would never deliver the briefing this asserts.
func TestStartRun_BackendParity(t *testing.T) {
	for _, backend := range []string{"claude-code", "mock"} {
		t.Run(backend, func(t *testing.T) {
			resetStrictness(t)
			sp := newFakeSpawner(map[string]fakeAgent{
				"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}, backend: backend},
			}, nil)
			c := newTestCoordinator(t, sp, nil)

			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
			require.NoError(t, err)

			require.Eventually(t, func() bool {
				sc := sp.chat(0)
				return sc != nil && len(sc.RecordedTexts()) == 1
			}, conformanceWait, 10*time.Millisecond, "backend %q must deliver the briefing via StartRun", backend)
			assert.Contains(t, sp.chat(0).RecordedTexts()[0], "do the thing")

			require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

			// Exactly one StartRun-path engine was spawned for this backend.
			assert.Equal(t, 1, sp.chatCount())

			// The journal records THIS backend as the run's harness (proves
			// plan.Backend rode the HarnessSpec unmodified, not coerced to
			// claude or dropped).
			require.Eventually(t, func() bool {
				var counts map[string]int
				c.items.View(func() { counts = c.itemsF.countsFor(out.RunID) })
				return counts["run_started"] == 1
			}, conformanceWait, 10*time.Millisecond, "backend %q must journal RunStarted on the migrated path", backend)
		})
	}
}

// TestStartRun_SendToIdleChildStartsTurn pins acceptance C1's turn delivery:
// a parent send to an IDLE migrated child is pushed down the RunChannel and
// becomes a NEW TURN on the engine (framed with sender + kind), and its
// consumption fact advances the durable mailbox cursor.
func TestStartRun_SendToIdleChildStartsTurn(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "second task", nil, "")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.RecordedTexts()) == 2
	}, conformanceWait, 10*time.Millisecond, "the send must start a new turn on the idle child")
	turn := sp.chat(0).RecordedTexts()[1]
	assert.Contains(t, turn, "second task")
	assert.Contains(t, turn, "kind="+KindMessage, "the kind survives as frame text, from the closed vocabulary (manly-grant (6))")
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
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinatorCap(t, sp, nil, 1) // pin cap=1: this test exercises D4 QUEUEING past the cap, not the (now-configurable) default cap value

	first, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.RecordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	second, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task two", "", "")
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

// TestStartRun_ResumeReadsTheSessionEntry pins acceptance C1's resume: after
// a child's run ends, a parent send resumes the harp as a FRESH run whose
// StartRun carries the native session key the harp's session ENTRY holds —
// the engine continues its own recorded session (no transcript re-priming).
// The entry is rebound after the run ends, so a resume that read any other
// copy of the key (the ended run's) is caught.
func TestStartRun_ResumeReadsTheSessionEntry(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	spawnCtx, spawnCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer spawnCancel()
	require.NoError(t, c.awaitChildUp(spawnCtx, out.Harp))
	// The key arrives async (the engine's own ev.Session, emitted after the
	// StartRun response awaitChildUp waited on); the resume's premise is that
	// it is already on the entry.
	require.Eventually(t, func() bool { return sp.NativeSession(out.Harp) == "native-sess-42" },
		conformanceWait, 10*time.Millisecond)

	sp.killEngine(0)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)
	sp.rebindNativeSession(out.Harp, "rotated-sess-7")

	// A later send resumes the harp as a fresh run... deterministically:
	// awaitChildUp replaces the wall-clock poll this used to be, blocking on
	// the SAME tracked resumeChild goroutine
	// AgentSend's driveQueued dispatches (armLaunch pre-registers the signal
	// synchronously before that dispatch, so this call cannot race it) —
	// keyed on the goroutine's own progress, not a 5s guess that flakes
	// under CPU starvation.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "carry on", nil, "")
	require.NoError(t, err)
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer awaitCancel()
	require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp), "the send to an ended harp must respawn it")
	require.Equal(t, 2, sp.chatCount(), "the respawned attempt must have reached StartEngine")
	// ...whose StartRun carried the entry's resume handle.
	require.Eventually(t, func() bool {
		sc := sp.chat(1)
		if sc == nil {
			return false
		}
		return len(sc.RecordedKeys()) == 1
	}, conformanceWait, 10*time.Millisecond)
	sc := sp.chat(1)
	assert.Equal(t, "rotated-sess-7", sc.RecordedKeys()[0], "resume must ride the key the session entry holds")

	// And the queued message arrives as the resumed engine's next turn.
	require.Eventually(t, func() bool {
		return len(sp.chat(1).RecordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Contains(t, sp.chat(1).RecordedTexts()[0], "carry on")
}
