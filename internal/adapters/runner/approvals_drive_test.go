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
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// askingEngine is an engine whose one turn makes a tool call and asks about
// it the way claude does under a permission host: the call goes out on the
// stream, then the engine calls the host and spawns the approval hook, and
// applies whatever the hook answers. It reaches the route through the Home
// it was bound on — what the session's endpoint serves.
type askingEngine struct {
	home    *fakeEngineHome
	hookOut chan []byte
	hostOut chan string
}

func (e *askingEngine) Exec([]present.Presentation) (engine.Exec, error) { return engine.Exec{}, nil }
func (e *askingEngine) Drivers() []engine.StructuredDriver               { return []engine.StructuredDriver{e} }
func (e *askingEngine) Resume(string) error                              { return nil }

func (e *askingEngine) Turn(ctx context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	send := func(ev agent.ChatEvent) {
		payload, _ := json.Marshal(ev)
		out <- engine.Event{Kind: ev.Kind(), Payload: payload}
	}
	send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolCallID: "t1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}})
	e.home.mu.Lock()
	route := e.home.approvalHost
	e.home.mu.Unlock()

	host := make(chan string, 1)
	go func() {
		out, _ := route.Host(ctx, json.RawMessage(`{"tool":"Bash","input":{"command":"ls"},"tool_use_id":"t1"}`))
		host <- out
	}()
	answer, err := route.Hook(ctx, "PermissionRequest", []byte(`{"tool":"Bash","input":{"command":"ls"}}`))
	if err != nil {
		return engine.TurnResult{}, err
	}
	e.hookOut <- answer
	send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: "t1", ToolOutput: "listed"}})
	e.hostOut <- <-host
	send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	return engine.TurnResult{NativeKey: "k"}, nil
}

// TestEngineHost_ATurnsAskReachesTheRootAndItsDecisionReturns is the
// runner's half of the route end to end, in process: the turn's tool call
// feeds the ledger from the live stream, the host anchors it, the hook binds
// to that anchor and parks ONE AgentRequest.approval on the coordinator,
// and the coordinator's decision is what the engine is handed — while the
// host, released by the call's result, answers only the superseded deny.
func TestEngineHost_ATurnsAskReachesTheRootAndItsDecisionReturns(t *testing.T) {
	home := &fakeEngineHome{}
	home.requestFn = func(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		ask := req.GetApproval()
		require.NotNil(t, ask, "the ask parks as AgentRequest.approval")
		assert.Equal(t, "Bash", ask.GetTool())
		assert.JSONEq(t, `{"command":"ls"}`, string(ask.GetInput()))
		assert.Equal(t, []string{"default"}, ask.GetTransitions(), "the engine's transitions ride the request")
		return &agentcoordpb.CoordinatorResponse{
			Status: coordgrpc.OKStatus(""),
			Kind:   &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{Allow: true, Decider: "human"}},
		}, nil
	}
	eng := &askingEngine{home: home, hookOut: make(chan []byte, 1), hostOut: make(chan string, 1)}
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	eh := NewEngineHost(context.Background(), nil, string(mock.Name), "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	require.NoError(t, eh.Drive(context.Background(), Turn{
		Launch:   launch.Launch{Engine: mock.Name, Mode: engine.Structured},
		Instance: eng, Prompt: "do it",
		approval: &approvalSpec{codec: codec, transitions: []string{"default"}, timeout: time.Minute},
	}))

	var hook mockAnswer
	select {
	case raw := <-eng.hookOut:
		require.NoError(t, json.Unmarshal(raw, &hook))
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never got an answer")
	}
	assert.True(t, hook.Allow, "the coordinator's decision is the engine's")

	select {
	case raw := <-eng.hostOut:
		var host mockAnswer
		require.NoError(t, json.Unmarshal([]byte(raw), &host))
		assert.False(t, host.Allow, "the host never answers allow")
		assert.Equal(t, errSuperseded.Error(), host.Message)
	case <-time.After(10 * time.Second):
		t.Fatal("the host never answered")
	}

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

// abandoningEngine's turn makes a call, has the host hold it, and ends —
// the engine process exits with the ask still open.
type abandoningEngine struct {
	askingEngine
}

func (e *abandoningEngine) Drivers() []engine.StructuredDriver { return []engine.StructuredDriver{e} }

func (e *abandoningEngine) Turn(ctx context.Context, _ engine.Exec, _ engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	payload, _ := json.Marshal(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolCallID: "t1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}})
	out <- engine.Event{Kind: "entry", Payload: payload}
	e.home.mu.Lock()
	route := e.home.approvalHost
	e.home.mu.Unlock()
	go func() {
		// The host outlives the turn's process here: background, so it can
		// only be released by the turn's end.
		out, _ := route.Host(context.Background(), json.RawMessage(`{"tool":"Bash","input":{"command":"ls"},"tool_use_id":"t1"}`))
		e.hostOut <- out
	}()
	// Wait until the host holds the call, then exit with it open.
	held := e.home.approvalHost.(*approvals)
	held.await(ctx, time.Minute, func() bool { return len(held.turn.slots) == 1 })
	return engine.TurnResult{NativeKey: "k"}, nil
}

// TestEngineHost_ATurnsEndReleasesItsHeldAsks: the turn's engine process
// ended with the host still holding its call; the drive ends the turn's
// correlation, and the host answers at once rather than holding on.
func TestEngineHost_ATurnsEndReleasesItsHeldAsks(t *testing.T) {
	home := &fakeEngineHome{}
	eng := &abandoningEngine{askingEngine{home: home, hostOut: make(chan string, 1)}}
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
	case raw := <-eng.hostOut:
		var host mockAnswer
		require.NoError(t, json.Unmarshal([]byte(raw), &host))
		assert.Equal(t, errTurnEnded.Error(), host.Message)
	case <-time.After(10 * time.Second):
		t.Fatal("the turn ended and its held host was never released")
	}
}
