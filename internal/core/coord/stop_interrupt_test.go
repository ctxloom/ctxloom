package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldChild starts one migrated child whose turns hold at gate, and returns
// once its first turn is provably inside the engine.
func heldChild(t *testing.T) (*Coordinator, *fakeSpawner, *RunOutcome) {
	t.Helper()
	resetStrictness(t)
	gate := make(chan struct{})
	sp := cutoverSpawner(0)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{Gate: gate} }
	c := newCutoverCoordinator(t, sp, 0)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "a long task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return midTurn(c, sp, out.Harp) }, conformanceWait, 10*time.Millisecond)
	return c, sp, out
}

// TestAgentStop_InterruptsTheRunningTurnThenEndsTheRun: a per-run agent_stop is
// interrupt-then-close, not a kill. The child's runner interrupts the turn in
// flight, which reaches its boundary and REPORTS — the parent reads that it
// was cut short — and the run ends as stopped. The report is on the child's
// out/ before the runner answers; the coordinator routes it like any other
// (doorbell, or the sweep backstop), so it is waited for, not assumed.
func TestAgentStop_InterruptsTheRunningTurnThenEndsTheRun(t *testing.T) {
	c, _, out := heldChild(t)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough", Grace: 5 * time.Second})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(ownerResultsFrom(t, out.Harp)) == 1 }, conformanceWait, 10*time.Millisecond,
		"the interrupted turn's report never reached the parent")
	assert.Contains(t, ownerResultsFrom(t, out.Harp)[0].Body, "interrupted before it finished")
	assert.Equal(t, StateEnded, rosterState(c, out.Harp))
	assert.Equal(t, CauseStopped, runCause(c, out.RunID))
}

// TestAgentStop_RunnerExitRacingTheStopKeepsTheStopsCause: the runner closes
// the run itself on StopRun, so its RunExited can reach the coordinator before
// the stop's own terminal does. The hook holds the stop until that exit has
// ENDED the run — the losing interleaving, forced — and the run's cause is
// still the stop, never "runner-exit".
func TestAgentStop_RunnerExitRacingTheStopKeepsTheStopsCause(t *testing.T) {
	c, _, out := heldChild(t)
	c.stopAnsweredHook = func(runID string) {
		require.Eventually(t, func() bool { return runCause(c, runID) != "" }, conformanceWait, 5*time.Millisecond,
			"the runner's own RunExited never ended the run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough"})
	require.NoError(t, err)
	assert.Equal(t, CauseStopped, runCause(c, out.RunID))
}

// TestControlSteer_InterruptMakesTheSteerTheNextTurnNow: a steer with
// interrupt cuts the child's running turn short, so the instruction is its
// next turn now — while the old turn was held and would never have reached
// its boundary on its own. The child is not stopped.
func TestControlSteer_InterruptMakesTheSteerTheNextTurnNow(t *testing.T) {
	c, sp, out := heldChild(t)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	_, err := c.ControlSteer(ctx, humanInitiator(), out.Harp, "change course", true)
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "change course")
	assert.NotEqual(t, StateEnded, rosterState(c, out.Harp), "an interrupt is not a stop")
}

// TestControlRequest_InterruptIsTheSteersAlone: no other control verb cuts a
// turn short, so asking one to is refused rather than silently ignored.
func TestControlRequest_InterruptIsTheSteersAlone(t *testing.T) {
	require.NoError(t, ControlRequest{Verb: ControlVerbSteer, Harp: "h", Body: "b", Interrupt: true}.Validate())
	for _, verb := range []string{ControlVerbQuestion, ControlVerbSummarize, ControlVerbPause, ControlVerbResume} {
		err := ControlRequest{Verb: verb, Harp: "h", Body: "b", Interrupt: true}.Validate()
		require.ErrorIs(t, err, ErrInvalidRequest, verb)
	}
}

// TestStopRequest_NegativeGraceIsRefused: a grace is a wait, and a negative
// one is a caller error rather than "no wait".
func TestStopRequest_NegativeGraceIsRefused(t *testing.T) {
	require.ErrorIs(t, StopRequest{Harp: "h", Grace: -time.Second}.Validate(), ErrInvalidRequest)
	require.NoError(t, StopRequest{Harp: "h", Grace: time.Second}.Validate())
}

// TestControlSteer_InterruptOnAnIdleTargetDoesNotCutTheSteer: the interrupt
// goes BEFORE the delivery. On an idle target there is nothing to cut, and
// the steer's own turn — held here at a re-armed gate — must finish as an
// ordinary turn, never be cut by the interrupt that came with it.
func TestControlSteer_InterruptOnAnIdleTargetDoesNotCutTheSteer(t *testing.T) {
	c, sp, out := heldChild(t)
	sc := sp.chat(0)
	held := make(chan struct{})
	sc.Mu.Lock()
	first := sc.Gate
	sc.Gate = held
	sc.Mu.Unlock()
	close(first)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.ControlSteer(ctx, humanInitiator(), out.Harp, "fresh orders", true)
	require.NoError(t, err)
	awaitChatText(t, sp, 0, "fresh orders")
	close(held)

	require.Eventually(t, func() bool { return len(ownerResultsFrom(t, out.Harp)) == 2 }, conformanceWait, 10*time.Millisecond)
	steered := ownerResultsFrom(t, out.Harp)
	for _, m := range steered {
		assert.NotContains(t, m.Body, "interrupted before it finished", "no turn was cut: the target was idle")
	}
}

// TestStopChildren_InterruptsEachRunningTurnThenCloses: the BULK agent_stop is
// interrupt-then-close too. A running child is interrupted and given its grace
// — its turn reaches its boundary and REPORTS that it was cut short — rather
// than being left to run until the drain bound forces it. The bound is set far
// past the caller's deadline, so a sweep that only waits for it fails here.
func TestStopChildren_InterruptsEachRunningTurnThenCloses(t *testing.T) {
	c, sp, out := heldChild(t)
	c.drainBound = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	res, err := c.Stop(ctx, ownerIdentity(), StopRequest{Reason: "fan-out complete", Grace: 5 * time.Second})
	require.NoError(t, err, "the sweep must settle on the interrupt, not wait out the drain bound")
	require.Len(t, res.Children, 1)
	assert.Equal(t, StopOutcomeStopped, res.Children[0].Outcome, "it ended inside the bound: nothing was forced")
	assert.Contains(t, res.Children[0].Detail, "fan-out complete")
	assert.Equal(t, CauseStopped, runCause(c, out.RunID))
	assert.Empty(t, readAuditKind(t, c, "drain_force"), "nothing waited for the bound: the interrupt ended the turn")

	results := recvKind(t, c, KindResult, conformanceWait)
	require.Len(t, results, 1, "the interrupted turn's report never reached the parent")
	assert.Contains(t, results[0].Body, "interrupted before it finished")
	awaitRelease(t, sp, 0)
}
