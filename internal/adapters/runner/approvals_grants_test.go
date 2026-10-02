package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// askByID has the route decide an ask that names its call, the way a
// pre-tool hook does — no host anchor needed — and returns the answer.
func askByID(t *testing.T, h *routeHarness, id string) mockAnswer {
	t.Helper()
	h.toolUse(id, "Bash", lsInput)
	raw, err := h.a.Hook(context.Background(), "PermissionRequest", []byte(`{"tool":"Bash","input":`+lsInput+`,"tool_use_id":"`+id+`"}`))
	require.NoError(t, err)
	return decodeAnswer(t, raw)
}

// TestApprovals_AnAllowsSessionRulesBecomeTheRunsGrants: the session rules an
// allow carries are held for the run — once each, in the order granted.
func TestApprovals_AnAllowsSessionRulesBecomeTheRunsGrants(t *testing.T) {
	answers := []engine.PermissionAnswer{
		{Allow: true, SessionRules: []string{"Bash(ls)", "Bash(ls)"}},
		{Allow: true, SessionRules: []string{"Read", "Bash(ls)"}},
	}
	h := newRouteHarness(t, func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
		ans := answers[0]
		answers = answers[1:]
		return ans, nil
	})
	assert.Empty(t, h.a.heldGrants(), "a run starts holding nothing")
	assert.True(t, askByID(t, h, "t1").Allow)
	assert.Equal(t, []string{"Bash(ls)"}, h.a.heldGrants())
	assert.True(t, askByID(t, h, "t2").Allow)
	assert.Equal(t, []string{"Bash(ls)", "Read"}, h.a.heldGrants())
}

// TestApprovals_OnlyAnAllowGrants: a deny carrying rules, and a decision
// that failed, grant nothing.
func TestApprovals_OnlyAnAllowGrants(t *testing.T) {
	for name, decide := range map[string]func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error){
		"deny": func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
			return engine.PermissionAnswer{Allow: false, SessionRules: []string{"Bash(ls)"}}, nil
		},
		"failed": func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
			return engine.PermissionAnswer{Allow: true, SessionRules: []string{"Bash(ls)"}}, errors.New("no coordinator")
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newRouteHarness(t, decide)
			h.toolUse("t1", "Bash", lsInput)
			// The answer itself is the codec's business (a deny carrying
			// rules does not even encode); what the run holds is the route's.
			_, _ = h.a.Hook(context.Background(), "PermissionRequest", []byte(`{"tool":"Bash","input":`+lsInput+`,"tool_use_id":"t1"}`))
			assert.Empty(t, h.a.heldGrants())
		})
	}
}

// TestApprovals_SetGrantsReplacesTheSet: the coordinator's set replaces the
// run's — a revoked rule is gone — and the held set is the run's own copy.
func TestApprovals_SetGrantsReplacesTheSet(t *testing.T) {
	h := newRouteHarness(t, func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
		return engine.PermissionAnswer{Allow: true, SessionRules: []string{"Bash(ls)", "Read"}}, nil
	})
	askByID(t, h, "t1")
	rules := []string{"Read"}
	h.a.setGrants(rules)
	rules[0] = "Write"
	held := h.a.heldGrants()
	assert.Equal(t, []string{"Read"}, held)
	held[0] = "Edit"
	assert.Equal(t, []string{"Read"}, h.a.heldGrants())
}

// postureEngine records each turn's posture. A turn prompted "ask" makes a
// call and has it decided through the route (a pre-tool-style ask naming
// its call); a turn prompted "hold" announces itself and waits to be let go.
type postureEngine struct {
	home     *fakeEngineHome
	postures chan engine.TurnPosture
	holding  chan struct{}
	release  chan struct{}
}

func (e *postureEngine) Exec([]present.Presentation) (engine.Exec, error) { return engine.Exec{}, nil }
func (e *postureEngine) Drivers() []engine.StructuredDriver               { return []engine.StructuredDriver{e} }
func (e *postureEngine) Resume(string) error                              { return nil }

func (e *postureEngine) Turn(ctx context.Context, _ engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	e.postures <- in.Posture
	send := func(ev agent.ChatEvent) {
		payload, _ := json.Marshal(ev)
		out <- engine.Event{Kind: ev.Kind(), Payload: payload}
	}
	switch in.Prompt {
	case "ask":
		send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolCallID: "t1", ToolName: "Bash", ToolInput: json.RawMessage(lsInput)}})
		e.home.mu.Lock()
		route := e.home.approvalRoute
		e.home.mu.Unlock()
		if _, err := route.Hook(ctx, "PermissionRequest", []byte(`{"tool":"Bash","input":`+lsInput+`,"tool_use_id":"t1"}`)); err != nil {
			return engine.TurnResult{}, err
		}
		send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: "t1", ToolOutput: "listed"}})
	case "hold":
		e.holding <- struct{}{}
		<-e.release
	}
	send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	return engine.TurnResult{NativeKey: "k"}, nil
}

// grantingRoot answers every ask with an allow granting rules.
func grantingRoot(home *fakeEngineHome, rules ...string) {
	home.requestFn = func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		return &agentcoordpb.CoordinatorResponse{
			Status: coordgrpc.OKStatus(""),
			Kind:   &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{Allow: true, SessionRules: rules, Decider: "human"}},
		}, nil
	}
}

func drivePostures(t *testing.T, approval bool) (*EngineHost, *postureEngine) {
	t.Helper()
	home := &fakeEngineHome{}
	grantingRoot(home, "Bash(ls)")
	eng := &postureEngine{home: home, postures: make(chan engine.TurnPosture, 8), holding: make(chan struct{}, 1), release: make(chan struct{})}
	eh := NewEngineHost(context.Background(), nil, string(mock.Name), "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	turn := Turn{Launch: launch.Launch{Engine: mock.Name, Mode: engine.Structured}, Instance: eng, Prompt: "plain"}
	if approval {
		turn.Prompt = "ask"
		codec, ok := mock.New().Approvals().Get()
		require.True(t, ok)
		turn.approval = &approvalSpec{codec: codec, timeout: time.Minute}
	}
	require.NoError(t, eh.Drive(context.Background(), turn))
	return eh, eng
}

func nextPosture(t *testing.T, eng *postureEngine) engine.TurnPosture {
	t.Helper()
	select {
	case p := <-eng.postures:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("no turn started")
		return engine.TurnPosture{}
	}
}

func turnReq(prompt string) *agentcoordpb.RunnerRequest {
	return &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_Turn{Turn: &agentcoordpb.Turn{Prompt: prompt}}}
}

func setGrantsReq(runID string, rules ...string) *agentcoordpb.RunnerRequest {
	return &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_SetGrants{SetGrants: &agentcoordpb.SetGrants{RunId: runID, Rules: rules}}}
}

// TestEngineHost_AGrantRidesEveryLaterTurnUntilRevoked: the human's allow for
// the session reaches the NEXT turn's posture (the engine's in-process rule
// dies with the turn's process); the coordinator's revoke — the run's
// remaining set — takes the rule off the turn after it.
func TestEngineHost_AGrantRidesEveryLaterTurnUntilRevoked(t *testing.T) {
	eh, eng := drivePostures(t, true)
	assert.Empty(t, nextPosture(t, eng).Grants, "the briefing turn starts with nothing granted")

	resp := eh.Handle(turnReq("plain"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Equal(t, []string{"Bash(ls)"}, nextPosture(t, eng).Grants, "the grant rides the next turn")

	resp = eh.Handle(setGrantsReq("run-1"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	resp = eh.Handle(turnReq("plain"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Empty(t, nextPosture(t, eng).Grants, "a revoked grant is off the next turn")
}

// TestEngineHost_ARevokeDuringATurnTakesTheNextOne forces the boundary race:
// the revoke lands while a turn is IN FLIGHT. That turn already runs at the
// posture it started with — its process was handed it — and the revoke
// takes effect from the turn after, never retroactively and never lost.
func TestEngineHost_ARevokeDuringATurnTakesTheNextOne(t *testing.T) {
	eh, eng := drivePostures(t, true)
	nextPosture(t, eng) // the briefing, which granted Bash(ls)

	held := make(chan *agentcoordpb.RunnerResponse, 1)
	go func() { held <- eh.Handle(turnReq("hold")) }()
	inFlight := nextPosture(t, eng)
	select {
	case <-eng.holding:
	case <-time.After(10 * time.Second):
		t.Fatal("the held turn never started")
	}
	resp := eh.Handle(setGrantsReq("run-1"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	close(eng.release)
	require.EqualValues(t, codes.OK, (<-held).GetStatus().GetCode())

	assert.Equal(t, []string{"Bash(ls)"}, inFlight.Grants, "the turn in flight keeps the posture it started at")
	resp = eh.Handle(turnReq("plain"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Empty(t, nextPosture(t, eng).Grants, "the revoke takes the next turn")
}

// TestEngineHost_SetGrantsRefusals: a set for another run is refused like
// every run-addressed request, and rules for a run that routes no approvals
// are refused — nothing could ever have granted them, so holding them would
// apply a grant no human gave this run. Clearing such a run's set is a no-op.
func TestEngineHost_SetGrantsRefusals(t *testing.T) {
	eh, eng := drivePostures(t, false)
	nextPosture(t, eng)

	assert.EqualValues(t, codes.PermissionDenied, eh.Handle(setGrantsReq("run-2")).GetStatus().GetCode(), "another run's set")
	assert.EqualValues(t, codes.FailedPrecondition, eh.Handle(setGrantsReq("run-1", "Bash(ls)")).GetStatus().GetCode(), "no approval route")
	assert.EqualValues(t, codes.OK, eh.Handle(setGrantsReq("run-1")).GetStatus().GetCode())
}
