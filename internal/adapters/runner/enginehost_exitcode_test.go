package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// exitTurn is one scripted turn's end: the status the engine process exited
// with on its own (nil: none — the turn ended it) and the turn's error.
type exitTurn struct {
	code *int
	err  error
}

// exitDriver is an engine whose turns answer (a session and a completion)
// and end as scripted, one exitTurn per turn in order.
type exitDriver struct {
	mu    sync.Mutex
	turns []exitTurn
}

func (d *exitDriver) Exec([]present.Presentation) (engine.Exec, error) { return engine.Exec{}, nil }
func (d *exitDriver) Drivers() []engine.StructuredDriver               { return []engine.StructuredDriver{d} }
func (d *exitDriver) Resume(string) error                              { return nil }
func (d *exitDriver) Turn(_ context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	d.mu.Lock()
	next := d.turns[0]
	d.turns = d.turns[1:]
	d.mu.Unlock()
	session, _ := json.Marshal(agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: "k", Resumable: true}})
	out <- engine.Event{Kind: "session", Payload: session}
	complete, _ := json.Marshal(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	out <- engine.Event{Kind: "complete", Payload: complete}
	return engine.TurnResult{NativeKey: "k", Answer: "a", ExitCode: next.code}, next.err
}

func exitCode(c int) *int { return &c }

// startExitRun starts a run over d and returns its home and cancel.
func startExitRun(t *testing.T, d *exitDriver) (*fakeEngineHome, context.CancelFunc) {
	t.Helper()
	home := &fakeEngineHome{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eh := newTestEngineHost(ctx, d, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	return home, cancel
}

// awaitRunCompleted is the run's terminal Result, once RunExited is reported.
func awaitRunCompleted(t *testing.T, home *fakeEngineHome) *agentcoordpb.Result {
	t.Helper()
	require.Eventually(t, func() bool { return home.exitCount() == 1 }, 5*time.Second, 5*time.Millisecond, "the run must end")
	completed := home.runCompleted()
	require.NotNil(t, completed, "the terminal RunCompleted must be emitted")
	return completed.GetResult()
}

// TestEngineHost_CleanTurnThenExit3_CarriesTheCodeNotAFailure: a turn that
// answered and whose engine then exited 3 is an ordinary boundary — the run
// parks, it does not end — and the code rides the run's terminal Result as
// information, the status exactly what it would be after an exit 0.
func TestEngineHost_CleanTurnThenExit3_CarriesTheCodeNotAFailure(t *testing.T) {
	home, cancel := startExitRun(t, &exitDriver{turns: []exitTurn{{code: exitCode(3)}}})
	require.Eventually(t, func() bool { return len(home.idleStops()) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "end_turn", home.idleStops()[0], "the turn reached an ordinary boundary")
	assert.Zero(t, home.exitCount(), "a non-zero exit after a clean result does not end the run")

	cancel()
	r := awaitRunCompleted(t, home)
	assert.Equal(t, agentcoordpb.Result_RUN_STATUS_CANCELLED, r.GetStatus(), "the code does not change the status")
	require.NotNil(t, r.ExitCode, "the engine's own exit status is carried")
	assert.Equal(t, int32(3), r.GetExitCode())
}

// TestEngineHost_CrashWithoutResult_FailsAndCarriesTheCode: a turn that died
// ends the run FAILED, and the status the engine died with rides along.
func TestEngineHost_CrashWithoutResult_FailsAndCarriesTheCode(t *testing.T) {
	home, _ := startExitRun(t, &exitDriver{turns: []exitTurn{{code: exitCode(3), err: errors.New("the turn's process died")}}})
	r := awaitRunCompleted(t, home)
	assert.Equal(t, agentcoordpb.Result_RUN_STATUS_FAILED, r.GetStatus())
	require.NotNil(t, r.ExitCode)
	assert.Equal(t, int32(3), r.GetExitCode())
}

// TestEngineHost_TurnWeEnded_CarriesNoCode: the code is the LAST turn's. A
// turn whose process the driver ended itself reports none, and an earlier
// turn's status must not stand in for it.
func TestEngineHost_TurnWeEnded_CarriesNoCode(t *testing.T) {
	home, cancel := startExitRun(t, &exitDriver{turns: []exitTurn{{code: exitCode(3)}, {code: nil}}})
	require.Eventually(t, func() bool { return len(home.idleStops()) == 1 }, 5*time.Second, 5*time.Millisecond)
	require.True(t, home.turnSink()(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "go on"}))
	require.Eventually(t, func() bool { return len(home.idleStops()) == 2 }, 5*time.Second, 5*time.Millisecond)

	cancel()
	r := awaitRunCompleted(t, home)
	assert.Nil(t, r.ExitCode, "the last turn's process was ended by ctxloom: no engine status to report")
}
