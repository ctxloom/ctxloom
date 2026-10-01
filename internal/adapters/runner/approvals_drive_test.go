package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// askingEngine is an engine whose one turn makes a tool call and asks about
// it the way claude does with no permission prompt tool: the call goes out on the
// stream, then the engine runs the approval hook and applies whatever it
// answers. It reaches the route through the Home it was bound on — what the
// session's endpoint serves.
type askingEngine struct {
	home    *fakeEngineHome
	hookOut chan []byte
}

func (e *askingEngine) Exec([]present.Presentation) (engine.Exec, error) { return engine.Exec{}, nil }
func (e *askingEngine) Drivers() []engine.StructuredDriver               { return []engine.StructuredDriver{e} }
func (e *askingEngine) Resume(string) error                              { return nil }

func (e *askingEngine) route() ApprovalRoute {
	e.home.mu.Lock()
	defer e.home.mu.Unlock()
	return e.home.approvalRoute
}

func sendEvent(out chan<- engine.Event, ev agent.ChatEvent) {
	payload, _ := json.Marshal(ev)
	out <- engine.Event{Kind: ev.Kind(), Payload: payload}
}

func (e *askingEngine) Turn(ctx context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	sendEvent(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolCallID: "t1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}})
	answer, err := e.route().Hook(ctx, wire.HookEventPermissionAsk, []byte(`{"tool":"Bash","input":{"command":"ls"}}`))
	if err != nil {
		return engine.TurnResult{}, err
	}
	e.hookOut <- answer
	sendEvent(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: "t1", ToolOutput: "listed"}})
	sendEvent(out, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	return engine.TurnResult{NativeKey: "k"}, nil
}

// TestEngineHost_ATurnsAskReachesTheRootAndItsDecisionReturns is the
// runner's half of the route end to end, in process: the turn's tool call
// feeds the ledger from the live stream, the hook matches it and parks ONE
// AgentRequest.approval on the coordinator — naming the call — and the
// coordinator's decision is what the engine is handed.
func TestEngineHost_ATurnsAskReachesTheRootAndItsDecisionReturns(t *testing.T) {
	home := &fakeEngineHome{}
	home.requestFn = func(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		ask := req.GetApproval()
		require.NotNil(t, ask, "the ask parks as AgentRequest.approval")
		assert.Equal(t, "Bash", ask.GetTool())
		assert.Equal(t, "t1", ask.GetToolUseId(), "the ledger names the call")
		assert.JSONEq(t, `{"command":"ls"}`, string(ask.GetInput()))
		require.Len(t, ask.GetTransitions(), 1, "the engine's transitions ride the request")
		tr := ask.GetTransitions()[0]
		assert.Equal(t, "default", tr.GetPosture(), "the posture token crosses")
		assert.Equal(t, "Ask before edits", tr.GetLabel(), "the engine's label crosses")
		assert.True(t, tr.GetDefault(), "the engine's default crosses")
		return &agentcoordpb.CoordinatorResponse{
			Status: coordgrpc.OKStatus(""),
			Kind:   &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{Allow: true, Decider: "human"}},
		}, nil
	}
	eng := &askingEngine{home: home, hookOut: make(chan []byte, 1)}
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	eh := NewEngineHost(context.Background(), nil, string(mock.Name), "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	require.NoError(t, eh.Drive(context.Background(), Turn{
		Launch:   launch.Launch{Engine: mock.Name, Mode: engine.Structured},
		Instance: eng, Prompt: "do it",
		approval: &approvalSpec{codec: codec, transitions: []engine.PostureTransition{{Posture: "default", Label: "Ask before edits", Default: true}}, timeout: time.Minute},
	}))

	var hook mockAnswer
	select {
	case raw := <-eng.hookOut:
		require.NoError(t, json.Unmarshal(raw, &hook))
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never got an answer")
	}
	assert.True(t, hook.Allow, "the coordinator's decision is the engine's")

	home.mu.Lock()
	defer home.mu.Unlock()
	approvals := 0
	for _, r := range home.requests {
		if r.GetApproval() != nil {
			approvals++
		}
	}
	assert.Equal(t, 1, approvals, "one ask, one parked request")
}

// abandoningEngine's turn makes a call, asks about it, and ends while the
// root is still deciding — the engine process exits with the ask open.
type abandoningEngine struct {
	askingEngine
	asked chan struct{}
	hook  chan []byte
}

func (e *abandoningEngine) Drivers() []engine.StructuredDriver { return []engine.StructuredDriver{e} }

func (e *abandoningEngine) Turn(_ context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	sendEvent(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolCallID: "t1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}})
	route := e.route()
	go func() {
		// The hook outlives the turn's process here: background, so only
		// the turn's end can answer it.
		answer, _ := route.Hook(context.Background(), wire.HookEventPermissionAsk, []byte(`{"tool":"Bash","input":{"command":"ls"}}`))
		e.hook <- answer
	}()
	<-e.asked // the root is deciding; exit with the ask open
	return engine.TurnResult{NativeKey: "k"}, nil
}

// TestEngineHost_ATurnsEndDeniesItsOpenAsks: the turn's engine process ended
// while the root was still deciding; the drive ends the turn's ledger, the
// root's wait is abandoned, and the ask is denied at once.
func TestEngineHost_ATurnsEndDeniesItsOpenAsks(t *testing.T) {
	home := &blockingHome{fakeEngineHome: &fakeEngineHome{}, done: make(chan error, 1)}
	eng := &abandoningEngine{askingEngine: askingEngine{home: home.fakeEngineHome}, asked: make(chan struct{}), hook: make(chan []byte, 1)}
	home.entered = eng.asked
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	eh := NewEngineHost(context.Background(), nil, string(mock.Name), "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	require.NoError(t, eh.Drive(context.Background(), Turn{
		Launch:   launch.Launch{Engine: mock.Name, Mode: engine.Structured},
		Instance: eng, Prompt: "do it",
		approval: &approvalSpec{codec: codec, timeout: time.Minute},
	}))
	select {
	case raw := <-eng.hook:
		var ans mockAnswer
		require.NoError(t, json.Unmarshal(raw, &ans))
		assert.False(t, ans.Allow)
		assert.Equal(t, errTurnEnded.Error(), ans.Message)
	case <-time.After(10 * time.Second):
		t.Fatal("the turn's end left the ask open")
	}
	require.ErrorIs(t, home.waitErr(t), context.Canceled, "the root's wait was abandoned with the turn")
}
