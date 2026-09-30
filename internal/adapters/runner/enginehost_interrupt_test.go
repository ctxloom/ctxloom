package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
)

// startGatedRun starts run-1 over a scripted chat whose turns HOLD at a gate
// after announcing the session — a turn provably mid-flight — and returns once
// the first turn is there.
func startGatedRun(t *testing.T) (*EngineHost, *fakeEngineHome, *scriptedChat) {
	t.Helper()
	home := &fakeEngineHome{}
	sc := &scriptedChat{Gate: make(chan struct{})}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode())
	// The session announcement is sent BEFORE the gate, so seeing it means
	// the turn is inside the driver, held.
	require.Eventually(t, func() bool { return home.customValue(coord.CustomHarnessSession) != nil }, 5*time.Second, 5*time.Millisecond)
	return eh, home, sc
}

// idleStops is every turn-idle event's stop_reason, in order.
func (f *fakeEngineHome) idleStops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.customs {
		if c.Name == coord.CustomTurnIdle {
			s, _ := c.Value["stop_reason"].(string)
			out = append(out, s)
		}
	}
	return out
}

// turnSink is the registered sink, read under the fake's lock.
func (f *fakeEngineHome) turnSink() func(*agentcoordpb.PeerMessage) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink
}

func (f *fakeEngineHome) exitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.exited)
}

func interruptReq(runID string) *agentcoordpb.RunnerRequest {
	return &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_InterruptRun{InterruptRun: &agentcoordpb.InterruptRun{RunId: runID}}}
}

// TestEngineHost_InterruptEndsTheTurnNotTheRun: InterruptRun cuts the turn in
// flight short — it ends at its boundary with stop_reason "interrupted", its
// report says so — and the run LIVES: nothing terminal is reported, and the
// next turn is a fresh engine process resumed by the key the interrupted one
// announced.
func TestEngineHost_InterruptEndsTheTurnNotTheRun(t *testing.T) {
	eh, home, sc := startGatedRun(t)

	resp := handleBounded(t, eh, interruptReq("run-1"))
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.Eventually(t, func() bool { return len(home.idleStops()) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"interrupted"}, home.idleStops())
	assert.Zero(t, home.exitCount(), "an interrupt is not the run's end")
	reports := home.turnReportsSeen()
	require.Len(t, reports, 1)
	assert.Contains(t, reports[0].Text, interruptedTurnNote, "the parent is told the turn was cut short, not that it produced nothing")

	close(sc.Gate)
	require.True(t, home.turnSink()(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "carry on"}))
	require.Eventually(t, func() bool { return len(home.idleStops()) == 2 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "end_turn", home.idleStops()[1])
	assert.Equal(t, []string{"", scriptedchat.NativeKey}, sc.RecordedKeys(), "the next turn resumes the interrupted session")
	assert.Zero(t, home.exitCount())
}

// TestEngineHost_InterruptWhileParkedIsANoOp: with no turn in flight there is
// nothing to cut short; the run stays parked and the next turn runs whole.
func TestEngineHost_InterruptWhileParkedIsANoOp(t *testing.T) {
	eh, home, sc := startGatedRun(t)
	close(sc.Gate)
	require.Eventually(t, func() bool { return len(home.idleStops()) == 1 }, 5*time.Second, 5*time.Millisecond)

	resp := handleBounded(t, eh, interruptReq("run-1"))
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode())
	require.True(t, home.turnSink()(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "next"}))
	require.Eventually(t, func() bool { return len(home.idleStops()) == 2 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"end_turn", "end_turn"}, home.idleStops(), "a stale interrupt does not reach a later turn")
}

// TestEngineHost_InterruptNamingAnotherRunIsRefused: the A9 correlation — a
// runner hosts one run, and a request naming another is refused, not applied.
func TestEngineHost_InterruptNamingAnotherRunIsRefused(t *testing.T) {
	eh, home, _ := startGatedRun(t)
	resp := handleBounded(t, eh, interruptReq("run-2"))
	assert.Equal(t, int32(codes.PermissionDenied), resp.GetStatus().GetCode())
	assert.Empty(t, home.idleStops(), "the turn in flight is untouched")
}

// TestEngineHost_StopRunInterruptsThenCloses: StopRun is interrupt-then-close,
// not a kill. The turn in flight is interrupted and reaches its boundary (its
// report and idle are written), THEN the run closes and reports its terminal.
// A turn queued behind the stopped one is never started.
func TestEngineHost_StopRunInterruptsThenCloses(t *testing.T) {
	eh, home, sc := startGatedRun(t)
	// Queued behind the held turn: the sink blocks until it would start.
	queued := make(chan bool, 1)
	go func() { queued <- home.turnSink()(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "queued"}) }()
	// The hand-off holds the enqueue lock across its wait for the held turn's
	// boundary: once the lock is taken, the queued turn is provably waiting
	// to start when the stop lands.
	require.Eventually(t, func() bool {
		if eh.enqueueMu.TryLock() {
			eh.enqueueMu.Unlock()
			return false
		}
		return true
	}, 5*time.Second, time.Millisecond)

	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StopRun{StopRun: &agentcoordpb.StopRun{
		RunId: "run-1", Grace: durationpb.New(5 * time.Second),
	}}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.NotNil(t, resp.GetStopRun())
	assert.Equal(t, []string{"interrupted"}, home.idleStops(), "the turn reached its boundary before the stop answered")

	require.Eventually(t, func() bool { return home.exitCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	select {
	case ok := <-queued:
		assert.False(t, ok, "the queued turn is refused, not started")
	case <-time.After(5 * time.Second):
		t.Fatal("the queued turn's hand-off never returned")
	}
	assert.Len(t, sc.RecordedTexts(), 1, "no turn ran after the stop")
}

// stubbornDriver is an engine that does not honour an interrupt: its turn
// holds until release, whatever its context says.
type stubbornDriver struct{ release chan struct{} }

func (d stubbornDriver) Exec([]present.Presentation) (engine.Exec, error) { return engine.Exec{}, nil }
func (d stubbornDriver) Drivers() []engine.StructuredDriver               { return []engine.StructuredDriver{d} }
func (d stubbornDriver) Resume(string) error                              { return nil }
func (d stubbornDriver) Turn(_ context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	payload, _ := json.Marshal(agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: "k", Resumable: true}})
	out <- engine.Event{Kind: "session", Payload: payload}
	<-d.release
	return engine.TurnResult{}, context.Canceled
}

// TestEngineHost_StopRunGraceBoundsTheWait: a turn that ignores the interrupt
// is waited on for the grace and no longer — the stop closes the run anyway.
func TestEngineHost_StopRunGraceBoundsTheWait(t *testing.T) {
	home := &fakeEngineHome{}
	d := stubbornDriver{release: make(chan struct{})}
	eh := newTestEngineHost(context.Background(), d, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	t.Cleanup(func() { close(d.release) })
	eh.BindHome(home)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool { return home.customValue(coord.CustomHarnessSession) != nil }, 5*time.Second, 5*time.Millisecond)

	start := time.Now()
	resp = handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StopRun{StopRun: &agentcoordpb.StopRun{
		RunId: "run-1", Grace: durationpb.New(100 * time.Millisecond),
	}}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode())
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond, "the grace was waited out")
	assert.Less(t, elapsed, 3*time.Second, "and no longer")
}

// TestEngineHost_MockHANGHonoursInterrupt: the mock engine's HANG turn — a
// stalled engine that says nothing until its context ends — is ended by
// InterruptRun at an ordinary boundary with stop_reason "interrupted", and
// the run takes its next turn whole (mock parity for the real interrupt).
func TestEngineHost_MockHANGHonoursInterrupt(t *testing.T) {
	inst, err := mock.New().Instance(engine.Session{Mode: engine.Structured, WorkDir: t.TempDir()})
	require.NoError(t, err)
	home := &fakeEngineHome{}
	eh := newTestEngineHost(context.Background(), inst, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	sr := testStartRun("run-1")
	l, err := coordgrpc.DecodeLaunch(sr.GetLaunch())
	require.NoError(t, err)
	l.Prompt = "HANG until interrupted"
	sr.Launch = coordgrpc.EncodeLaunch(l)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: sr}})
	require.Equal(t, int32(codes.OK), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	// HANG relays nothing, so the held turn is observed through the host
	// itself: busy, with a turn context to cut.
	require.Eventually(t, func() bool {
		eh.mu.Lock()
		defer eh.mu.Unlock()
		return eh.inTurn && eh.turnCancel != nil
	}, 5*time.Second, 5*time.Millisecond)

	require.Equal(t, int32(codes.OK), handleBounded(t, eh, interruptReq("run-1")).GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(home.idleStops()) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"interrupted"}, home.idleStops())

	require.True(t, home.turnSink()(&agentcoordpb.PeerMessage{MessageId: "m-2", Text: "go on"}))
	require.Eventually(t, func() bool { return len(home.idleStops()) == 2 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "end_turn", home.idleStops()[1])
	assert.Zero(t, home.exitCount())
}
