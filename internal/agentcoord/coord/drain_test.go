package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
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

	// Reach a live, journaled run: at least the run_started item must have
	// landed before we manufacture a synthetic run_completed for the SAME
	// (run_id, channel) — this is what makes the hook's injected event
	// indistinguishable from a genuine late arrival.
	require.Eventually(t, func() bool {
		var seq uint64
		c.mu.Lock()
		if ch := c.chans[out.Harp]; ch != nil {
			seq = ch.ackSeq
		}
		c.mu.Unlock()
		return seq > 0
	}, conformanceWait, 5*time.Millisecond, "the RunChannel must be live and have processed at least one event")

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
	c.drainHook = func(role string) {
		defer close(hookFired)
		if role != out.Harp {
			return
		}
		c.mu.Lock()
		ch := c.chans[role]
		seq := ch.ackSeq + 1
		c.mu.Unlock()
		require.NotNil(t, ch)
		c.handleAgentEvent(ch, &agentcoordpb.AgentEvent{
			RunId: out.RunID,
			Seq:   seq,
			Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
				Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_SUCCEEDED},
			}},
		})
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
