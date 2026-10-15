package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

func outcomeState(events chan *agentcoordpb.AgentEvent) *runState {
	return &runState{ownedRun: &ownedRunSession{
		outcome: &coord.RunOutcome{RunID: "run-outcome"},
		events:  events,
	}}
}

// TestOwnedRunOutcome_CoordinatorsTerminalEndsTheRunWithItsStatus: a run
// whose engine never sent its RunCompleted — its runner was killed, or lost —
// ends on the one the coordinator synthesizes (seq 0, coord.endRun), and
// `ctxloom run` exits with the status it carries rather than reporting a run
// that "never reported its outcome".
func TestOwnedRunOutcome_CoordinatorsTerminalEndsTheRunWithItsStatus(t *testing.T) {
	events := make(chan *agentcoordpb.AgentEvent, 1)
	code := int32(137)
	events <- &agentcoordpb.AgentEvent{RunId: "run-outcome", Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
		Result: &agentcoordpb.Result{
			Status:   agentcoordpb.Result_RUN_STATUS_FAILED,
			Error:    nil,
			ExitCode: &code,
		},
	}}}

	err := outcomeState(events).ownedRunOutcome(137)

	var status clifmt.ExitStatus
	require.True(t, errors.As(err, &status), "the run exits with its recorded status, got %v", err)
	assert.Equal(t, 137, status.Code)
}

// TestOwnedRunOutcome_MissingTerminalNamesTheRunnersStatus: if no terminal
// arrives at all, the error says how the runner process ended — a killed
// runner and a clean exit are not the same report.
func TestOwnedRunOutcome_MissingTerminalNamesTheRunnersStatus(t *testing.T) {
	prev := ownerRunCompletionWait
	ownerRunCompletionWait = 50 * time.Millisecond
	t.Cleanup(func() { ownerRunCompletionWait = prev })

	err := outcomeState(make(chan *agentcoordpb.AgentEvent)).ownedRunOutcome(137)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "never reported its outcome")
	assert.Contains(t, err.Error(), "status 137")
}

// TestOwnedRunOutcome_ATerminalStillInFlightIsWaitedFor: the terminal event
// may land a moment after the runner's exit; the default wait takes it,
// rather than reporting a run that never reported its outcome.
func TestOwnedRunOutcome_ATerminalStillInFlightIsWaitedFor(t *testing.T) {
	events := make(chan *agentcoordpb.AgentEvent)
	code := int32(3)
	go func() {
		time.Sleep(100 * time.Millisecond)
		events <- &agentcoordpb.AgentEvent{RunId: "run-outcome", Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_FAILED, ExitCode: &code},
		}}}
	}()

	err := outcomeState(events).ownedRunOutcome(0)

	var status clifmt.ExitStatus
	require.ErrorAs(t, err, &status, "the late terminal ends the run, got %v", err)
	assert.Equal(t, 3, status.Code)
}
