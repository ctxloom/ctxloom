package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// TestTerminateRun_DrainsInFlightRunCompleted is D4's deterministic
// reproduction of the terminal-tail drop race (damp-pupil 1 — "C1's dropped
// run_completed item is the repro shape"): the runner's production emitter
// (enginehost.go's adapt) sends a run's FINAL run_completed item on the
// RunChannel, then reports RunExited on the SEPARATE RunnerChannel back to
// back — two independent streams, no ordering guarantee. Reproducing the
// worst-case interleaving via real network scheduling would be flaky by
// construction, so this test drives the exact race deterministically: a
// drainHook fires synchronously at the instant terminateRun begins its D4
// drain wait (drainTerminalTail) and, from inside it, delivers the
// run_completed item — simulating "the frame was in flight and lands exactly
// during the drain window". Without the fix (severChan running immediately
// on RunExited, no wait), this ordering is exactly what drops the item; with
// the fix, drainTerminalTail's wait on ch.completed absorbs it.
func TestTerminateRun_DrainsInFlightRunCompleted(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// Reach a live, journaled AND QUIESCENT run before manufacturing a
	// synthetic run_completed for the SAME (run_id, channel) — quiescence is
	// what makes the hook's injected event indistinguishable from a genuine
	// late arrival AND what makes the seq it picks safe.
	//
	// This used to wait for `ackSeq > 0`, which the FIRST standup event
	// (run_started, seq 1) satisfies while the SECOND (the engine's
	// harness-session custom event, seq 2) is still in flight. The hook below
	// then samples ackSeq, computes ackSeq+1, releases c.mu, and by the time
	// handleAgentEvent re-takes it the real seq 2 has landed — so the
	// injection is deduped as a duplicate (`seq <= ch.ackSeq`, runchannel.go),
	// ch.completed never closes, the drain burns its whole 500ms window and
	// the items fold holds zero run_completed. That is exactly the "expected
	// 1, actual 0" at 0.53s this test produced on the merge path: not a slow
	// condition — it costs ~0.05s loaded — but the fixture racing the run's
	// own standup, with the budget merely hiding it.
	//
	// The journaled harness session id is the join: it is written from the
	// LAST event standup emits. The scripted engine is parked on turnGate from
	// its first turn onward and no emitter here is timer-driven, so once that
	// fact is durable the channel is genuinely quiet and ackSeq+1 is a seq
	// nothing else can claim.
	require.Eventually(t, func() bool { return harnessSessionID(c, out.Harp) != "" }, conformanceWait, 5*time.Millisecond,
		"the RunChannel must be live and have processed the whole of standup")

	var credHash string
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			credHash = r.CredHash
		}
	})
	require.NotEmpty(t, credHash)

	// Arm the deterministic race: the hook fires the instant terminateRun
	// (for CauseRunnerExit) starts draining — deliver the run's terminal
	// item from inside it, exactly the "arrives during the drain window"
	// shape the fix is built to absorb.
	hookFired := make(chan struct{})
	// injected records that the synthetic frame was actually TAKEN by the
	// channel rather than deduped away. Written and read on the test's own
	// goroutine: drainTerminalTail calls the hook synchronously, inside the
	// handleRunExited call below. Without it, a fixture that loses its
	// injection is indistinguishable from the production drop this test
	// exists to catch — which is how the last occurrence cost a triage cycle.
	var injected bool
	c.drainHook = func(role string) {
		defer close(hookFired)
		if role != out.Harp {
			return
		}
		c.mu.Lock()
		ch := c.chans[role]
		c.mu.Unlock()
		require.NotNil(t, ch)
		c.mu.Lock()
		seq := ch.ackSeq + 1
		c.mu.Unlock()
		c.handleAgentEvent(ch, &agentcoordpb.AgentEvent{
			RunId: out.RunID,
			Seq:   seq,
			Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
				Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_SUCCEEDED},
			}},
		})
		// ch.completed closes exactly when a run_completed item is journaled
		// on this channel, so it is the one unambiguous receipt.
		select {
		case <-ch.completed:
			injected = true
		default:
		}
	}

	// The explicit RunExited path (CauseRunnerExit) — the ONLY cause the fix
	// scopes to (see drainTerminalTail's doc). Bypasses the network
	// entirely: this is the coordinator-side handler the RunnerChannel recv
	// loop calls, driven directly so the race is deterministic, not a real
	// scheduler gamble.
	c.handleRunExited(credHash, &agentcoordpb.RunExited{
		RunId:             out.RunID,
		TerminalEventSeen: true,
	})

	select {
	case <-hookFired:
	default:
		t.Fatal("drainHook never fired — terminateRun did not reach the D4 drain wait for CauseRunnerExit")
	}
	require.True(t, injected,
		"the fixture's synthetic run_completed was deduped by the channel watermark, so this run never posed the question the test asks — a fixture fault, NOT the C1 drop")

	var counts map[string]int
	c.items.View(func() { counts = c.itemsF.countsFor(out.RunID) })
	assert.Equal(t, 1, counts["run_completed"],
		"the run_completed item injected DURING the drain window must survive to the items fold — this is the C1 drop reproduced and fixed")

	var ended bool
	c.runs.View(func() {
		if r := c.runsF.run(out.RunID); r != nil {
			ended = r.Ended
		}
	})
	assert.True(t, ended, "the run must still terminate normally once the drain resolves")

	close(gate) // release the scripted engine's gated turn so t.Cleanup can tear down cleanly
}

// TestDrainTracked_SkipsARunThatEndedBeforeTheDrainBegan pins drainTracked's
// skip branch directly: a harp whose CURRENT run had already ended before
// drainTracked ever runs must not be tracked. A mutant that turns the guard's
// `||` into `&&` lets an ended run through — it would then be reported under
// DrainOutcome.Exited, which documents "every child that was LIVE when the
// drain began" (folds.go's RunRecord.Ended is exactly the signal that says it
// was not).
func TestDrainTracked_SkipsARunThatEndedBeforeTheDrainBegan(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	harp := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, harp) == StateIdle }, conformanceWait, 5*time.Millisecond,
		"precondition: the child must be idle before it is ended, so ending it is not confused with the drain's own idle-end path")

	var runID string
	c.runs.View(func() { runID = c.runsF.currentRun(harp).RunID })
	c.terminateRun(runID, CauseStopped, "ended well before any drain began")
	require.Equal(t, StateEnded, rosterState(c, harp), "precondition: the run is ended before drainTracked ever runs")

	tracked := c.drainTracked(nil)
	for _, ch := range tracked {
		assert.NotEqual(t, harp, ch.harp,
			"a run that had already ended before the drain began must not be tracked — it would surface in DrainOutcome.Exited, which promises only children live at drain start")
	}
}

// TestDrainTracked_SkipsARosterHarpWhoseCurrentRunIsNil pins drainTracked's
// other half of the same guard: a roster entry whose currentRun lookup comes
// back nil (a harp on the §6a roster with no matching run record — the same
// defensive case livenessTargets already guards against) must be skipped
// without dereferencing it. A mutant that turns the guard's leading `||` into
// `&&` evaluates r.Ended on that nil r before the nil check can short it out,
// which panics.
func TestDrainTracked_SkipsARosterHarpWhoseCurrentRunIsNil(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const ghost = "ghost-harp"
	c.runs.View(func() {
		// A roster entry with no counterpart in runsF: currentRun(ghost)
		// resolves to nil, exactly the shape drainTracked must tolerate.
		c.rosterF.entries[ghost] = &RosterEntry{Harp: ghost, State: StateExecuting}
	})

	var tracked []drainChild
	require.NotPanics(t, func() { tracked = c.drainTracked(nil) },
		"a roster harp with no current run must be skipped, not dereferenced")
	for _, ch := range tracked {
		assert.NotEqual(t, ghost, ch.harp, "a harp with no current run must never be tracked")
	}
}
