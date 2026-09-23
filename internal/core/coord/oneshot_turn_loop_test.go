package coord

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oneShotSpawner is startRunSpawner's one-shot sibling: a migrated,
// resume-capable agent resolved to ResumeModeOneShot, backed by a scriptedChat
// that live-confirms the loadSession capability (resumable) so the turn loop
// actually tears the engine down at each boundary.
func oneShotSpawner(mk func() *scriptedChat) *fakeSpawner {
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"},
			backend: "claude-code", oneshot: true},
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

// TestSlotYield_MidTurnParkYieldsSlotToPeer is the §6a SLOT-YIELD gate: a
// child parked mid-turn consumes no compute, so its execution slot goes back
// to the pool and a peer queued behind the concurrency cap runs on it.
//
// The mechanism is NOT load-sensitive, which is why this proof cannot be
// masked by a slow machine: if the slot were not yielded (cap 1, A parked and
// only unparked AFTER the assertion) B could never leave StateQueued at any
// budget — a hard deadlock, not a slow arrival. B is held mid-turn by a
// turnGate so "B executing while A parked" is a stable, observable instant
// rather than a state B might race through before a poll sees it.
func TestSlotYield_MidTurnParkYieldsSlotToPeer(t *testing.T) {
	resetStrictness(t)
	aGate := make(chan struct{})
	bGate := make(chan struct{})
	var spawns int
	sp := startRunSpawner(func() *scriptedChat {
		spawns++
		if spawns == 1 {
			return &scriptedChat{Gate: aGate} // A: held mid-turn, then parks
		}
		return &scriptedChat{Gate: bGate} // B: held mid-turn until released
	})
	c := newTestCoordinatorCap(t, sp, nil, 1) // cap 1: B can only run if A yields its slot

	a, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task A", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateExecuting }, conformanceWait, 10*time.Millisecond,
		"precondition: A holds the only slot")

	// A parks MID-TURN in agent_recv and — crucially — yields its slot while
	// it waits.
	aRecv := make(chan []Message, 1)
	go func() {
		msgs, _ := childRecv(t, c, a.RunID, conformanceWait)
		aRecv <- msgs
	}()
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateParked }, conformanceWait, 10*time.Millisecond,
		"A must be parked (slot yielded) while it waits in agent_recv")

	// B, queued behind the cap-1 ceiling, acquires the FREED slot and reaches
	// StateExecuting — the direct proof A yielded. B is gated mid-turn, so it
	// holds this state for a stable observation. If the slot were NOT yielded
	// B would sit in StateQueued forever (A holds the only slot and is
	// unparked below, AFTER this), so no wall-clock budget can mask a real
	// starvation.
	b, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task B", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, b.Harp) == StateExecuting }, conformanceWait, 10*time.Millisecond,
		"B must reach StateExecuting (acquire the slot A yielded) while A is parked")

	// Both invariants hold at the same instant: B executing, A still parked —
	// B did not have to wait for A.
	assert.Equal(t, StateExecuting, rosterState(c, b.Harp), "B must be executing on the yielded slot")
	assert.Equal(t, StateParked, rosterState(c, a.Harp), "A must still be parked")

	// Release B: it runs its turn to completion (secondary confirmation; the
	// slot-yield claim is already proven above and does not hinge on this
	// engine-turn latency).
	close(bGate)
	bRes := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task B") }, conformanceWait)
	require.NotEmpty(t, bRes, "B completes its turn once released")

	// A is still parked after B has come and gone.
	assert.Equal(t, StateParked, rosterState(c, a.Harp), "A must still be parked")

	// Unpark A: its recv completes, it reclaims a slot and finishes its turn.
	_, err = c.AgentSend(ownerIdentity(), a.Harp, KindMessage, "carry on", nil, "")
	require.NoError(t, err)
	select {
	case msgs := <-aRecv:
		require.Len(t, msgs, 1, "A's parked recv must complete with the send that unparked it")
	case <-time.After(conformanceWait):
		t.Fatal("A's parked agent_recv never completed after the send")
	}
	close(aGate)
	aRes := recvWhere(t, c, func(m Message) bool { return m.Kind == "result" && strings.Contains(m.Body, "task A") }, conformanceWait)
	require.NotEmpty(t, aRes, "A resumes and completes its turn once unparked")
}

// countRuns returns how many run records the live fold currently holds.
func countRuns(c *Coordinator) int {
	n := 0
	c.runs.View(func() { n = len(c.runsF.runs) })
	return n
}

// TestReapEndedRuns_KeepsCurrentAndTail is the DETERMINISTIC unit test of the
// retention reap: manufacturing several ended runs for one harp directly on
// the journal (each an idle-reaped incarnation), reapEndedRuns must keep the
// harp's CURRENT run plus the newest EndedRunTail ended runs
// and drop the rest — the fold-level guarantee a long-lived harp's
// incarnations rely on, tested without engine timing.
func TestReapEndedRuns_KeepsCurrentAndTail(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	c, err := New(Options{
		ProjectDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		Spawner:      newFakeSpawner(nil, nil),
		EndedRunTail: 2, // keep the newest 2 ended runs (beyond the current one)
		OwnerHarp:    ownerIdentity().Harp,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	const harp = "child-harp-X"
	base := time.Now().Add(-time.Minute) // recent: not max-age reaped
	// Six runs for one harp, oldest→newest; the last is the harp's current run
	// (byHarp points to the latest factRunEnqueued).
	runIDs := make([]string, 6)
	for i := range runIDs {
		id := fmt.Sprintf("run-x-%d", i)
		runIDs[i] = id
		at := base.Add(time.Duration(i) * time.Second)
		require.NoError(t, c.runs.Exec(func() ([]Fact, error) {
			return []Fact{
				factAt(factRunEnqueued, at, runEnqueued{RunID: id, Harp: harp, Agent: "worker", CredHash: id + "-cred", Depth: 1}),
				factAt(factRunEnded, at, runEnded{RunID: id, Cause: CauseIdleReaped}),
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

	// Idempotent: a second reap at the bounded floor changes nothing.
	c.reapEndedRuns()
	assert.Equal(t, 3, countRuns(c))
}

// TestRetention_BoundsFoldGrowthAcrossResumes is the integration half of the
// reap: the wiring terminateRun → reapEndedRuns must keep the live fold
// bounded as one harp accumulates ended run after ended run. It uses an
// in-process engine whose process ENDS the run after each turn (EndAfterTurns:1)
// — so every mailbox delivery resumes the harp as a fresh, promptly-ended incarnation,
// the churn a repeatedly reaped harp produces. With tail=1 the fold must never
// grow one record per resume.
func TestRetention_BoundsFoldGrowthAcrossResumes(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(
		map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}},
		func() *scriptedChat { return &scriptedChat{EndAfterTurns: 1} }, // its run ends after each turn
	)
	teeHome(t)
	c, err := New(Options{
		ProjectDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		Spawner:      sp,
		EndedRunTail: 1, // keep the current run + exactly one ended audit tail
		OwnerHarp:    ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond,
		"the engine ends after its turn, ending the run")

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

// TestOneShot_PersistentModeUnchanged proves the one-shot park is inert for
// a conversational (ResumeModePersistent) child: its engine stays WARM across
// turns — the same process handles turn 2, no teardown, no resume.
func TestOneShot_PersistentModeUnchanged(t *testing.T) {
	resetStrictness(t)
	// A resume-capable, live-confirmed engine but a PERSISTENT plan: the
	// runner's park decision reads the launch identity's OneShot, so the
	// boundary must NOT end the engine process.
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"},
			backend: "claude-code"}, // oneshot:false
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.RecordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	// The turn boundary parks the child idle (warm), NOT ended.
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond,
		"a persistent child parks idle at the boundary, engine warm")

	// A second turn is handled by the SAME runner (no second spawn): a fresh
	// engine process resumed by the key turn 1 reported.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "again", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.RecordedTexts()) == 2
	}, conformanceWait, 10*time.Millisecond, "the parked runner handles turn 2 itself")
	assert.Equal(t, 1, sp.chatCount(), "a persistent child must never spawn a second runner for a follow-up turn")
	keys := sp.chat(0).RecordedKeys()
	require.Len(t, keys, 2)
	assert.Empty(t, keys[0], "the first turn resumes nothing")
	assert.Equal(t, scriptedchat.NativeKey, keys[1], "the follow-up turn's process resumes by the key the first reported")
}
