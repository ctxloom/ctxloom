package coord

import (
	"context"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldChild starts one migrated child whose turns hold at gate, and returns
// once its first turn is provably inside the engine.
func heldChild(t *testing.T) (*Coordinator, *fakeSpawner, *RunOutcome) {
	t.Helper()
	resetStrictness(t)
	gate := make(chan struct{})
	sp := cutoverSpawner(t, 0)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{Gate: gate} }
	c := newCutoverCoordinator(t, sp, 0)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "a long task", "")
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

// TestAgentStop_ReportsTheInterruptedTurnWhenItsDoorbellIsLost forces the
// losing interleaving of a stop. The runner writes the interrupted turn's
// report, rings, and only then answers StopRun — on a DIFFERENT channel. The
// answer can win: the coordinator ends the run and cuts its run channel before
// it has read the ring, and the ring dies with the channel. The handler below
// discards every ring, which is exactly that outcome; the sweep stays at the
// production cadence, so it cannot be what delivers. The report must still
// reach the parent within the wait.
func TestAgentStop_ReportsTheInterruptedTurnWhenItsDoorbellIsLost(t *testing.T) {
	c, _, out := heldChild(t)
	c.SetSpoolDoorbellHandler(func(string, spool.Ref) {})
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()

	_, err := c.Stop(ctx, ownerIdentity(), StopRequest{Harp: out.Harp, Reason: "enough", Grace: 5 * time.Second})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(ownerResultsFrom(t, out.Harp)) == 1 }, conformanceWait, 10*time.Millisecond,
		"the run's channel was cut with its ring unread, and nothing swept the report it announced")
	assert.Contains(t, ownerResultsFrom(t, out.Harp)[0].Body, "interrupted before it finished")
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

	req := ControlRequest{Verb: ControlVerbSteer, Harp: out.Harp, Body: "change course", Interrupt: true}
	steered, err := c.Control(ctx, humanInitiator(), req)
	require.NoError(t, err)
	assert.Equal(t, DeliveryInterrupted, steered.Delivery,
		"alpha issue 9: an interrupt that took effect must not report \"queued\" like plain mail")
	assert.Contains(t, steered.Disposition, "running turn", "the result carries the outcome in words, not only the mode")
	said := controlDisposition(req, steered)
	assert.Contains(t, said, "interrupted", "the initiator's text says the turn was cut")
	assert.NotContains(t, said, "queued")
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
	outcome, err := c.ControlSteer(ctx, humanInitiator(), out.Harp, "fresh orders", true)
	require.NoError(t, err)
	assert.Equal(t, DeliveryNewTurn, outcome.Delivery, "nothing was running to cut: the idle target is woken, and the answer says so")
	assert.NotContains(t, outcome.Disposition, "interrupted")
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

// TestInterruptVerdict_SaysOnlyWhatTheRunnerConfirmed: the steer's outcome is
// what the runner ANSWERED. A cut it confirmed is "interrupted"; a parked run
// it confirmed is nothing cut; an answer that never came, or that does not
// say, is unconfirmed — never silently "queued"; a refusal is the steer's
// error.
func TestInterruptVerdict_SaysOnlyWhatTheRunnerConfirmed(t *testing.T) {
	v, err := interruptVerdict(RunnerResponse{Kind: InterruptRunResult{Interrupted: true}}, nil)
	require.NoError(t, err)
	assert.Equal(t, interruptCut, v)

	v, err = interruptVerdict(RunnerResponse{Kind: InterruptRunResult{}}, nil)
	require.NoError(t, err)
	assert.Equal(t, interruptNone, v)

	v, err = interruptVerdict(RunnerResponse{}, context.DeadlineExceeded)
	require.NoError(t, err, "an unanswered interrupt does not fail the steer: the steer is still written")
	assert.Equal(t, interruptUnconfirmed, v)

	v, err = interruptVerdict(RunnerResponse{}, nil)
	require.NoError(t, err)
	assert.Equal(t, interruptUnconfirmed, v, "an answer that does not say what happened confirms nothing")

	_, err = interruptVerdict(RunnerResponse{Err: ErrControlRefused}, nil)
	require.ErrorIs(t, err, ErrControlRefused)
}

// TestSteerOutcome_InterruptWordsTheOutcome: the outcome a steer reports once
// its interrupt is judged — the cut and the unconfirmed cases replace the
// mail wording; nothing cut keeps it.
func TestSteerOutcome_InterruptWordsTheOutcome(t *testing.T) {
	queued := SteerOutcome{Delivery: DeliveryQueued, Disposition: "queued mid-turn", MessageID: "m-1"}

	cut := queued.afterInterrupt(interruptCut)
	assert.Equal(t, DeliveryInterrupted, cut.Delivery)
	assert.Contains(t, cut.Disposition, "interrupted")
	assert.Equal(t, "m-1", cut.MessageID, "the withdraw handle survives")

	unknown := queued.afterInterrupt(interruptUnconfirmed)
	assert.Equal(t, DeliveryInterruptUnconfirmed, unknown.Delivery)
	assert.Contains(t, unknown.Disposition, "not known")
	assert.NotContains(t, unknown.Disposition, "queued mid-turn", "an unknown outcome must not borrow the mail wording")

	assert.Equal(t, queued, queued.afterInterrupt(interruptNone))
}

// TestControlDisposition_SteerSaysWhatHappened: the text a wire caller reads
// for a steer is the outcome's own wording, not a bare mode in parentheses.
func TestControlDisposition_SteerSaysWhatHappened(t *testing.T) {
	got := controlDisposition(ControlRequest{Verb: ControlVerbSteer, Harp: "kid"},
		ControlResult{Verb: ControlVerbSteer, Delivery: DeliveryInterrupted, Disposition: "interrupted its running turn"})
	assert.Equal(t, "steered kid: interrupted its running turn", got)
}
