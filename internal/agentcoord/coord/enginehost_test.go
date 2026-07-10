package coord

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fakeEngineHome records everything the engine host emits, standing in for a
// dialed Home.
type fakeEngineHome struct {
	mu      sync.Mutex
	events  []*agentcoordpb.AgentEvent
	customs []struct {
		Name  string
		Value map[string]any
	}
	sink   func(*agentcoordpb.PeerMessage) bool
	exited []struct {
		Code      int
		SessionID string
		Terminal  bool
	}
	seq uint64
}

func (f *fakeEngineHome) emitEvent(ev *agentcoordpb.AgentEvent) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.events = append(f.events, ev)
	return f.seq
}

func (f *fakeEngineHome) emitCustomEvent(name string, value map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customs = append(f.customs, struct {
		Name  string
		Value map[string]any
	}{name, value})
}

func (f *fakeEngineHome) SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink
}

func (f *fakeEngineHome) ReportRunExited(code int, sessionID string, terminal bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exited = append(f.exited, struct {
		Code      int
		SessionID string
		Terminal  bool
	}{code, sessionID, terminal})
}

func (f *fakeEngineHome) customNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.customs))
	for i, c := range f.customs {
		out[i] = c.Name
	}
	return out
}

func (f *fakeEngineHome) payloadKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ev := range f.events {
		out = append(out, itemKind(ev))
	}
	return out
}

// scriptedChat is a StructuredChat whose turns are scripted: each received
// text yields a Session event (first turn only), a thinking entry, an
// assistant echo, a tool_use/tool_result pair, and a Complete.
type scriptedChat struct {
	mu       sync.Mutex
	requests []agent.ChatRequest
	texts    []string
	turnGate chan struct{} // non-nil: turns block until released
}

func (s *scriptedChat) recordedTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

func (s *scriptedChat) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	s.mu.Lock()
	s.requests = append(s.requests, req)
	gate := s.turnGate
	s.mu.Unlock()
	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: "native-sess-42"}}) {
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Text == "" {
				continue
			}
			s.mu.Lock()
			s.texts = append(s.texts, msg.Text)
			s.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "pondering"}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "echo: " + msg.Text}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "grep", ToolInput: []byte(`{"q":"x"}`)}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolOutput: "found"}}) ||
				!send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", InputTokens: 10, CostUSD: 0.0000015}}) {
				return ctx.Err()
			}
		}
	}
}

func testStartRun(runID string) *agentcoordpb.StartRun {
	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness:     "claude-code",
		Model:       "claude-sonnet-5",
		Workspace:   "/work",
		SessionHarp: "child-harp-1",
		Permission:  agent.PermissionBypass,
	})
	if err != nil {
		panic(err)
	}
	input, _ := structpb.NewStruct(map[string]any{"prompt": "CTX\n\ndo the thing"})
	return &agentcoordpb.StartRun{RunId: runID, Harness: spec, Input: input, Role: "worker"}
}

// TestEngineHost_StartRunDrivesChatInProcess pins the whole runner half of
// C1: StartRun decodes the spec, launches the backend's Chat IN-PROCESS, the
// briefing rides the first turn, native events adapt onto plane-1
// (RunStarted → turn_started → message/tool items → turn_idle), and the
// native session id is reported via the harness_session custom event.
func TestEngineHost_StartRunDrivesChatInProcess(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), "StartRun must succeed: %s", resp.GetStatus().GetMessage())
	require.NotNil(t, resp.GetStartRun())
	assert.NotZero(t, resp.GetStartRun().GetPid())

	// The briefing (context pre-joined) is the first turn, verbatim.
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "CTX\n\ndo the thing", sc.recordedTexts()[0])

	// The turn's native events adapted onto plane-1.
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the turn boundary must reach plane-1")

	kinds := home.payloadKinds()
	assert.Contains(t, kinds, "run_started")
	assert.Contains(t, kinds, "message_started")
	assert.Contains(t, kinds, "message_delta")
	assert.Contains(t, kinds, "message_completed")
	assert.Contains(t, kinds, "tool_call_started")
	assert.Contains(t, kinds, "tool_call_completed")
	names := home.customNames()
	assert.Contains(t, names, CustomHarnessSession, "the native session id reaches the coordinator's journal path")
	assert.Contains(t, names, CustomTurnStarted)

	// The chat request the backend saw matches the decoded spec.
	sc.mu.Lock()
	req := sc.requests[0]
	sc.mu.Unlock()
	assert.Equal(t, "/work", req.WorkDir)
	assert.Equal(t, "claude-sonnet-5", req.Model)
	assert.Equal(t, agent.PermissionBypass, req.Permissions)
}

// TestEngineHost_TurnSinkDeliversFramedMail: a coordinator-pushed
// PeerMessage lands on the engine as a NEW TURN, framed with sender + kind
// (manly-grant (6)).
func TestEngineHost_TurnSinkDeliversFramedMail(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	home.mu.Lock()
	sink := home.sink
	home.mu.Unlock()
	require.NotNil(t, sink, "startRun must register the turn sink")

	structured, _ := structpb.NewStruct(map[string]any{"kind": "task"})
	ok := sink(&agentcoordpb.PeerMessage{MessageId: "m-9", FromAgentId: "parent-harp", Text: "next assignment", Structured: structured})
	require.True(t, ok)
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 2 }, 5*time.Second, 10*time.Millisecond)
	got := sc.recordedTexts()[1]
	assert.Contains(t, got, "[coordinator-delivered message from=parent-harp kind=task]")
	assert.Contains(t, got, "next assignment")
}

// TestEngineHost_StartRunIdempotentOnReissue: the SAME run_id reissued
// (reconnect) returns the cached result; a DIFFERENT run is refused
// (max_concurrent_runs=1), and a mismatched A9 correlation is refused.
func TestEngineHost_StartRunIdempotentOnReissue(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	eh.BindHome(home)

	mismatch := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-OTHER")}})
	assert.NotEqual(t, int32(0), mismatch.GetStatus().GetCode(), "A9: a run this runner was not spawned for is refused")

	first := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), first.GetStatus().GetCode())
	again := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	assert.Same(t, first, again, "a reissued StartRun (same run_id) returns the cached result")
}

// TestEngineHost_ChatEndEmitsRunCompletedAndRunExited: when the engine's
// stream ends, the adapter emits the terminal RunCompleted (usage in
// micro-USD) and reports RunExited with the native session id.
func TestEngineHost_ChatEndEmitsRunCompletedAndRunExited(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	ctx, cancel := context.WithCancel(context.Background())
	eh := NewEngineHost(ctx, sc, "claude-code", "run-1")
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode())
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	cancel() // kill the engine: Chat returns, the stream closes

	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
	}, 5*time.Second, 10*time.Millisecond, "chat end must report RunExited on the lifecycle link")

	home.mu.Lock()
	defer home.mu.Unlock()
	assert.Equal(t, "native-sess-42", home.exited[0].SessionID)
	assert.True(t, home.exited[0].Terminal)
	var completed *agentcoordpb.RunCompleted
	for _, ev := range home.events {
		if rc := ev.GetRunCompleted(); rc != nil {
			completed = rc
		}
	}
	require.NotNil(t, completed, "the terminal RunCompleted must be emitted")
	require.NotNil(t, completed.GetResult().GetUsage())
	assert.Equal(t, uint64(10), completed.GetResult().GetUsage().GetInputTokens())
	// 0.0000015 USD → 1.5 micros → round-half-even → 2.
	assert.Equal(t, uint64(2), completed.GetResult().GetUsage().GetCostUsdMicros(), "money converts ONCE at the runner boundary, round-half-even")
	assert.Equal(t, uint32(1), completed.GetResult().GetNumTurns())
}
