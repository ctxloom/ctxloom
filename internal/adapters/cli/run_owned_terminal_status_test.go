package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestRenderOwnedRunEvents_TerminalStatusFailsClosed pins a fix: the
// terminal Result.RunStatus enum used to be read as a DENY-list — anything
// that was not RUN_STATUS_FAILED exited 0. The zero value
// RUN_STATUS_UNSPECIFIED therefore read as success at every consumer, and so
// did CANCELLED, TIMED_OUT and BUDGET_EXCEEDED: a run that was killed, ran
// out of time, or blew its budget reported success to the shell.
//
// The correct shape is the one ApprovalDecision already uses
// (enginehost.go's interactionResolution): an explicit SUCCESS allow-list,
// everything else — including any value proto3's open enums let through —
// is a failure.
func TestRenderOwnedRunEvents_TerminalStatusFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   agentcoordpb.Result_RunStatus
		wantExit bool
	}{
		{"succeeded", agentcoordpb.Result_RUN_STATUS_SUCCEEDED, false},
		{"failed", agentcoordpb.Result_RUN_STATUS_FAILED, true},
		{"cancelled", agentcoordpb.Result_RUN_STATUS_CANCELLED, true},
		{"timed_out", agentcoordpb.Result_RUN_STATUS_TIMED_OUT, true},
		{"budget_exceeded", agentcoordpb.Result_RUN_STATUS_BUDGET_EXCEEDED, true},
		{"unspecified (the zero value)", agentcoordpb.Result_RUN_STATUS_UNSPECIFIED, true},
		{"unknown future value", agentcoordpb.Result_RunStatus(99), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			events := make(chan *agentcoordpb.AgentEvent, 1)
			events <- &agentcoordpb.AgentEvent{
				RunId: "run-1",
				Payload: &agentcoordpb.AgentEvent_RunCompleted{
					RunCompleted: &agentcoordpb.RunCompleted{
						Result: &agentcoordpb.Result{Status: tc.status, Text: "done"},
					},
				},
			}

			_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, "run-1", events, nil, true)
			if !tc.wantExit {
				assert.NoError(t, err)
				return
			}
			var exit clifmt.ExitStatus
			if !assert.ErrorAs(t, err, &exit, "terminal status %v must not exit 0", tc.status) {
				return
			}
			assert.Equal(t, 1, exit.Code)
		})
	}
}

// TestRenderOwnedRunEvents_MissingResultFailsClosed: a RunCompleted with no
// Result at all is the same fail-open hole one level up — GetResult() returns
// nil and the old code fell straight through to `return nil`.
func TestRenderOwnedRunEvents_MissingResultFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events := make(chan *agentcoordpb.AgentEvent, 1)
	events <- &agentcoordpb.AgentEvent{
		RunId:   "run-1",
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}},
	}
	_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, "run-1", events, nil, true)
	var exit clifmt.ExitStatus
	require.ErrorAs(t, err, &exit, "a terminal event with no Result must not report success")
	assert.Equal(t, 1, exit.Code)
}

// TestRenderOwnedRunEvents_FailedRunExitsWithTheEngineStatus: a run that did
// not succeed exits with the engine's own status when the engine produced one
// — its code, or 128+signum for a signal — so a script wrapping `ctxloom run`
// sees what it would have seen running the engine directly. With no engine
// status, or a zero one on a run that still failed, the failure is ctxloom's
// own classification and exits 1 rather than 0.
func TestRenderOwnedRunEvents_FailedRunExitsWithTheEngineStatus(t *testing.T) {
	code := func(v int32) *int32 { return &v }
	for _, tc := range []struct {
		name   string
		status agentcoordpb.Result_RunStatus
		exit   *int32
		want   int
	}{
		{"engine exit code", agentcoordpb.Result_RUN_STATUS_FAILED, code(3), 3},
		{"engine killed by a signal", agentcoordpb.Result_RUN_STATUS_FAILED, code(143), 143},
		{"no engine status", agentcoordpb.Result_RUN_STATUS_FAILED, nil, 1},
		{"zero status on a failed run", agentcoordpb.Result_RUN_STATUS_FAILED, code(0), 1},
		{"cancelled, no engine status", agentcoordpb.Result_RUN_STATUS_CANCELLED, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			events := make(chan *agentcoordpb.AgentEvent, 1)
			events <- &agentcoordpb.AgentEvent{
				RunId: "run-1",
				Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
					Result: &agentcoordpb.Result{Status: tc.status, ExitCode: tc.exit},
				}},
			}
			_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, "run-1", events, nil, true)
			var exit clifmt.ExitStatus
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, tc.want, exit.Code)
		})
	}
}

// TestRenderOwnedRunEvents_CancelledRunSaysItWasStopped: a run cancelled with
// no error message (a stop or an interrupt whose reason the engine's own
// result did not carry) says so in words, not as a bare proto enum name.
func TestRenderOwnedRunEvents_CancelledRunSaysItWasStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	defer restore()

	events := make(chan *agentcoordpb.AgentEvent, 1)
	events <- &agentcoordpb.AgentEvent{
		RunId: "run-1",
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_CANCELLED},
		}},
	}
	_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, "run-1", events, nil, true)
	var exit clifmt.ExitStatus
	require.ErrorAs(t, err, &exit)
	assert.Contains(t, sink.String(), runCancelledNotice)
	assert.NotContains(t, sink.String(), "RUN_STATUS_")
}

// TestRenderOwnedRunEvents_CancelledRunNamesItsCause: a cancelled run whose
// result carries its cause (the coordinator fills in who stopped it and why)
// says it was cancelled AND why — not "failed", and not the generic notice,
// which is only for a cancellation nobody explained.
func TestRenderOwnedRunEvents_CancelledRunNamesItsCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	defer restore()

	const cause = "run ended (stopped): stopped by owner-harp: milestone landed"
	events := make(chan *agentcoordpb.AgentEvent, 1)
	events <- &agentcoordpb.AgentEvent{
		RunId: "run-1",
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_CANCELLED, Error: &rpcstatus.Status{Message: cause}},
		}},
	}
	_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, "run-1", events, nil, true)
	var exit clifmt.ExitStatus
	require.ErrorAs(t, err, &exit)
	assert.Contains(t, sink.String(), "run was cancelled: "+cause)
	assert.NotContains(t, sink.String(), "run failed")
	assert.NotContains(t, sink.String(), runCancelledNotice)
}

// TestRenderOwnedRunEvents_EachEndSaysItsOwnWords: a failed run with a
// message says it FAILED with that message (not that it was cancelled), and
// a completion carrying no result at all says so — each end its own words.
func TestRenderOwnedRunEvents_EachEndSaysItsOwnWords(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  *agentcoordpb.Result
		want    string
		notWant string
	}{
		{"failed with a message", &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_FAILED, Error: &rpcstatus.Status{Message: "engine crashed"}},
			"run failed: engine crashed", "cancelled"},
		{"no result", nil, "run ended with no terminal result", "run failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var sink strings.Builder
			restore := clidiag.SetSink(&sink)
			defer restore()

			events := make(chan *agentcoordpb.AgentEvent, 1)
			events <- &agentcoordpb.AgentEvent{
				RunId:   "run-1",
				Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{Result: tc.result}},
			}
			_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, "run-1", events, nil, true)
			var exit clifmt.ExitStatus
			require.ErrorAs(t, err, &exit)
			assert.Contains(t, sink.String(), tc.want)
			assert.NotContains(t, sink.String(), tc.notWant)
		})
	}
}
