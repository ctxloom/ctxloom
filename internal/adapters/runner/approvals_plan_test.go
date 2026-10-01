package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// The posture the tests' plans are approved at: the mock's one transition.
const approvedPosture = "default"

// planAnswer is the mock codec's answer, mode change included.
type planAnswer struct {
	Allow   bool   `json:"allow"`
	SetMode string `json:"set_mode"`
	Message string `json:"message"`
}

func decodePlanAnswer(t *testing.T, raw []byte) planAnswer {
	t.Helper()
	var a planAnswer
	require.NoError(t, json.Unmarshal(raw, &a), "%s", raw)
	return a
}

// presentPlan has the route decide a plan the call id names, as the plan
// tool's pre-tool hook does.
func presentPlan(t *testing.T, h *routeHarness, id string) planAnswer {
	t.Helper()
	input := `{"plan":"ship-it"}`
	h.toolUse(id, mock.PlanTool, input)
	raw, err := h.a.Hook(context.Background(), wire.HookEventPreTool, []byte(`{"tool":"`+mock.PlanTool+`","input":`+input+`,"tool_use_id":"`+id+`"}`))
	require.NoError(t, err)
	return decodePlanAnswer(t, raw)
}

// askAbout has the route decide a call the id names; suggests is the mode
// change the engine offers with it ("" for none).
func askAbout(t *testing.T, h *routeHarness, id, tool, suggests string) planAnswer {
	t.Helper()
	h.toolUse(id, tool, `{}`)
	raw, err := h.a.Hook(context.Background(), wire.HookEventPermissionAsk, []byte(`{"tool":"`+tool+`","input":{},"tool_use_id":"`+id+`","suggests_set_mode":"`+suggests+`"}`))
	require.NoError(t, err)
	return decodePlanAnswer(t, raw)
}

// scriptedRoot answers asks in order from answers and records the tool of
// each ask it was asked.
type scriptedRoot struct {
	answers []engine.PermissionAnswer
	asked   []string
}

func (r *scriptedRoot) decide(_ context.Context, ask engine.PermissionAsk) (engine.PermissionAnswer, error) {
	r.asked = append(r.asked, ask.Tool)
	ans := r.answers[0]
	r.answers = r.answers[1:]
	return ans, nil
}

func approvePlan() engine.PermissionAnswer {
	return engine.PermissionAnswer{Allow: true, SetMode: engine.Provide(approvedPosture)}
}

// TestApprovals_APlansPostureIsHeldNotAnswered: the posture the human
// approved a plan at is the run's from then on; the plan's own answer is an
// allow that carries no mode change (an engine leaves plan mode on its own
// terms, and a pre-tool answer cannot change mode).
func TestApprovals_APlansPostureIsHeldNotAnswered(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{approvePlan()}}
	h := newRouteHarness(t, root.decide)
	assert.Empty(t, h.a.heldMode(), "a run starts at its declared posture")
	ans := presentPlan(t, h, "p1")
	assert.True(t, ans.Allow)
	assert.Empty(t, ans.SetMode, "the plan's answer changes no mode")
	assert.Equal(t, approvedPosture, h.a.heldMode())
}

// TestApprovals_TheSuggestedTransitionAfterAPlanIsAnsweredUnasked: the first
// call after the approval whose engine suggests the approved posture is
// allowed with that mode change, nobody asked — the human already approved
// executing at it. Once only: the next such call is the human's again.
func TestApprovals_TheSuggestedTransitionAfterAPlanIsAnsweredUnasked(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{approvePlan(), {Allow: true}}}
	h := newRouteHarness(t, root.decide)
	presentPlan(t, h, "p1")

	ans := askAbout(t, h, "e1", "Edit", approvedPosture)
	assert.Equal(t, planAnswer{Allow: true, SetMode: approvedPosture}, ans)
	assert.Equal(t, []string{mock.PlanTool}, root.asked, "nobody was asked about the edit")

	askAbout(t, h, "e2", "Edit", approvedPosture)
	assert.Equal(t, []string{mock.PlanTool, "Edit"}, root.asked, "the transition is taken once")
}

// TestApprovals_AnotherCallAfterAPlanGoesToTheHumanWithTheModeAttached: a
// call that does not suggest the approved posture goes to the human as
// usual; their allow carries the pending mode change. A deny leaves it
// pending, for the next call.
func TestApprovals_AnotherCallAfterAPlanGoesToTheHumanWithTheModeAttached(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{approvePlan(), {Allow: false, Message: "no"}, {Allow: true}, {Allow: true}}}
	h := newRouteHarness(t, root.decide)
	presentPlan(t, h, "p1")

	assert.Equal(t, planAnswer{Message: "no"}, askAbout(t, h, "b1", "Bash", ""), "a deny carries no mode")
	assert.Equal(t, planAnswer{Allow: true, SetMode: approvedPosture}, askAbout(t, h, "b2", "Bash", ""), "the mode rides the human's allow")
	assert.Equal(t, planAnswer{Allow: true}, askAbout(t, h, "b3", "Bash", ""), "and only the first")
	assert.Equal(t, []string{mock.PlanTool, "Bash", "Bash", "Bash"}, root.asked)
}

// TestApprovals_AnUnapprovedPlanMovesNothing: a rejected plan, and one whose
// decision failed, leave the run's posture and pending transition alone.
func TestApprovals_AnUnapprovedPlanMovesNothing(t *testing.T) {
	for name, decide := range map[string]func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error){
		"rejected": func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
			return engine.PermissionAnswer{Allow: false, Message: "split it", SetMode: engine.Provide(approvedPosture)}, nil
		},
		"failed": func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
			return approvePlan(), errNoDecision
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newRouteHarness(t, decide)
			assert.False(t, presentPlan(t, h, "p1").Allow)
			assert.Empty(t, h.a.heldMode())
			assert.False(t, h.a.hasPendingMode())
		})
	}
}

// TestApprovals_AnAllowsModeChangeIsHeld: a mode change the human allows for
// a call — the engine's own suggestion, granted for the session — is the
// run's posture from then on, as a plan's is.
func TestApprovals_AnAllowsModeChangeIsHeld(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{{Allow: true, SetMode: engine.Provide(approvedPosture)}}}
	h := newRouteHarness(t, root.decide)
	assert.Equal(t, planAnswer{Allow: true, SetMode: approvedPosture}, askAbout(t, h, "e1", "Edit", approvedPosture))
	assert.Equal(t, approvedPosture, h.a.heldMode())
	assert.False(t, h.a.hasPendingMode(), "only a plan leaves a transition pending")
}

// TestApprovals_TurnEndDropsThePendingTransitionNotThePosture: the pending
// transition belongs to the process that presented the plan; the next turn
// starts at the held posture, so there is nothing left to switch.
func TestApprovals_TurnEndDropsThePendingTransitionNotThePosture(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{approvePlan(), {Allow: true}}}
	h := newRouteHarness(t, root.decide)
	presentPlan(t, h, "p1")
	require.True(t, h.a.hasPendingMode())
	h.a.endTurn()
	assert.False(t, h.a.hasPendingMode())
	assert.Equal(t, approvedPosture, h.a.heldMode())
	assert.Equal(t, planAnswer{Allow: true}, askAbout(t, h, "e1", "Edit", approvedPosture), "the next turn's edit is the human's")
}

// TestApprovals_AnEditDuringAParkedPlanIsTheHumans forces the race: an edit
// the engine suggests the posture for arrives while the plan is still
// parked with the human. Nothing is approved yet, so the edit is asked
// about — it is never taken as the plan's transition.
func TestApprovals_AnEditDuringAParkedPlanIsTheHumans(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	input := `{"plan":"ship-it"}`
	h.toolUse("p1", mock.PlanTool, input)
	plan := make(chan []byte, 1)
	go func() {
		raw, _ := h.a.Hook(context.Background(), wire.HookEventPreTool, []byte(`{"tool":"`+mock.PlanTool+`","input":`+input+`,"tool_use_id":"p1"}`))
		plan <- raw
	}()
	require.Equal(t, mock.PlanTool, recvAsk(t, root).Tool, "the plan is parked")

	h.toolUse("e1", "Edit", `{}`)
	edit := make(chan []byte, 1)
	go func() {
		raw, _ := h.a.Hook(context.Background(), wire.HookEventPermissionAsk, []byte(`{"tool":"Edit","input":{},"tool_use_id":"e1","suggests_set_mode":"`+approvedPosture+`"}`))
		edit <- raw
	}()
	require.Equal(t, "Edit", recvAsk(t, root).Tool, "the edit went to the human while the plan was parked")

	root.release <- approvePlan()
	assert.True(t, decodePlanAnswer(t, <-plan).Allow)
	root.release <- engine.PermissionAnswer{Allow: true}
	assert.Equal(t, planAnswer{Allow: true, SetMode: approvedPosture}, decodePlanAnswer(t, <-edit),
		"approved meanwhile: the human's allow is the first call after the plan, and carries its mode")
}

// TestApprovals_ThePlansTransitionIsPendingWhenItsAnswerIsReleased is the
// other boundary, tested directly: the settle that releases a plan's
// approval — to its owner and to every POST joined to it — is the one that
// leaves the transition pending, under the same lock. Were it applied after
// the release, an edit the engine sends the moment it applies the approval
// could reach the human first.
func TestApprovals_ThePlansTransitionIsPendingWhenItsAnswerIsReleased(t *testing.T) {
	h := newRouteHarness(t, (&scriptedRoot{}).decide)
	d := &decision{done: make(chan struct{})}
	h.a.settle(d, engine.PermissionAsk{Kind: engine.AskPlan, Tool: mock.PlanTool}, approvePlan(), nil)
	require.True(t, isClosed(d.done))
	assert.True(t, h.a.hasPendingMode())
	assert.Equal(t, approvedPosture, h.a.heldMode())
}

func recvAsk(t *testing.T, b *blockingDecide) engine.PermissionAsk {
	t.Helper()
	select {
	case ask := <-b.asks:
		return ask
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was asked")
		return engine.PermissionAsk{}
	}
}

// TestEngineHost_AnApprovedPlansPostureStartsEveryLaterTurn: the engine's
// mode does not survive its process, so the posture the human approved the
// plan at reaches each later turn as the turn's own mode — never the turn
// that presented the plan, which was handed its posture when it started.
func TestEngineHost_AnApprovedPlansPostureStartsEveryLaterTurn(t *testing.T) {
	eh, eng := drivePostures(t, true)
	eng.home.mu.Lock()
	eng.home.requestFn = func(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		d := &agentcoordpb.ApprovalDecision{Allow: true, Decider: "human"}
		if req.GetApproval().GetKind() == agentcoordpb.ApprovalRequest_APPROVAL_KIND_PLAN {
			d.SetMode = approvedPosture
		}
		return &agentcoordpb.CoordinatorResponse{Status: coordgrpc.OKStatus(""), Kind: &agentcoordpb.CoordinatorResponse_Approval{Approval: d}}, nil
	}
	eng.home.mu.Unlock()
	nextPosture(t, eng) // the briefing: an ask, allowed with no mode change

	resp := eh.Handle(turnReq("plan"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Empty(t, nextPosture(t, eng).Mode, "the plan's own turn runs at the declared posture")

	for range 2 {
		resp = eh.Handle(turnReq("plain"))
		require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		assert.Equal(t, approvedPosture, nextPosture(t, eng).Mode)
	}
}

// TestApprovals_ClaudePlanToAcceptEdits drives claude's own codec through the
// route, with its live payload shapes: the plan approved at acceptEdits is a
// PreToolUse allow (which claude refuses to carry a mode change on — it is
// held instead), and the next edit, whose PermissionRequest suggests
// setMode acceptEdits, is allowed with that session mode change, nobody
// asked.
func TestApprovals_ClaudePlanToAcceptEdits(t *testing.T) {
	codec, ok := claude.Claude{}.Approvals().Get()
	require.True(t, ok)
	root := &scriptedRoot{answers: []engine.PermissionAnswer{{Allow: true, SetMode: engine.Provide("acceptEdits")}}}
	h := newRouteHarness(t, root.decide)
	h.a.codec = codec

	plan := `{"plan":"# Plan","planFilePath":"/p.md"}`
	h.toolUse("toolu_plan", "ExitPlanMode", plan)
	raw, err := h.a.Hook(context.Background(), "PreToolUse", []byte(`{"hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":`+plan+`,"tool_use_id":"toolu_plan"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":`+plan+`}}`, string(raw))
	h.toolResult("toolu_plan")

	edit := `{"file_path":"/w/a.go","old_string":"a","new_string":"b"}`
	h.toolUse("toolu_edit", "Edit", edit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held := make(chan error, 1)
	go func() {
		held <- h.a.hold(ctx, json.RawMessage(`{"tool_name":"Edit","input":`+edit+`,"tool_use_id":"toolu_edit"}`))
	}()
	raw, err = h.a.Hook(context.Background(), "PermissionRequest", []byte(`{"hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":`+edit+`,"permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}}}`, string(raw))
	assert.Equal(t, []string{"ExitPlanMode"}, root.asked, "nobody was asked about the edit")
	assert.Equal(t, "acceptEdits", h.a.heldMode())
	h.toolResult("toolu_edit")
	assert.ErrorIs(t, <-held, errSuperseded)
}

// TestApprovals_ASecondPlanIsNeverTheFirstsTransition: only a call's ask can
// take the pending transition unasked — a plan presented after an approved
// one goes to the human whatever its engine suggests with it.
func TestApprovals_ASecondPlanIsNeverTheFirstsTransition(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{approvePlan(), {Allow: false, Message: "no"}}}
	h := newRouteHarness(t, root.decide)
	presentPlan(t, h, "p1")
	input := `{"plan":"ship-more"}`
	h.toolUse("p2", mock.PlanTool, input)
	raw, err := h.a.Hook(context.Background(), wire.HookEventPreTool, []byte(`{"tool":"`+mock.PlanTool+`","input":`+input+`,"tool_use_id":"p2","suggests_set_mode":"`+approvedPosture+`"}`))
	require.NoError(t, err)
	assert.False(t, decodePlanAnswer(t, raw).Allow)
	assert.Equal(t, []string{mock.PlanTool, mock.PlanTool}, root.asked, "the second plan was the human's")
}

// TestApprovals_AHumansOwnModeChangeBeatsThePendingTransition: after a plan
// approved at acceptEdits, the human allows the next call with a mode change
// of their own — theirs is the one carried and held, and the pending one is
// spent.
func TestApprovals_AHumansOwnModeChangeBeatsThePendingTransition(t *testing.T) {
	codec, ok := claude.Claude{}.Approvals().Get()
	require.True(t, ok)
	root := &scriptedRoot{answers: []engine.PermissionAnswer{
		{Allow: true, SetMode: engine.Provide("acceptEdits")},
		{Allow: true, SetMode: engine.Provide("default")},
	}}
	h := newRouteHarness(t, root.decide)
	h.a.codec = codec
	plan := `{"plan":"# Plan"}`
	h.toolUse("toolu_plan", "ExitPlanMode", plan)
	_, err := h.a.Hook(context.Background(), "PreToolUse", []byte(`{"tool_name":"ExitPlanMode","tool_input":`+plan+`,"tool_use_id":"toolu_plan"}`))
	require.NoError(t, err)

	cmd := `{"command":"make"}`
	h.toolUse("toolu_bash", "Bash", cmd)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.a.hold(ctx, json.RawMessage(`{"tool_name":"Bash","input":`+cmd+`,"tool_use_id":"toolu_bash"}`)) }()
	raw, err := h.a.Hook(context.Background(), "PermissionRequest", []byte(`{"tool_name":"Bash","tool_input":`+cmd+`}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"default","destination":"session"}]}}}`, string(raw))
	assert.Equal(t, "default", h.a.heldMode())
	assert.False(t, h.a.hasPendingMode())
}

// TestApprovals_ASuggestionWiderThanTheApprovedPostureIsTheHumans: a plan
// approved at default leaves default pending; an edit whose engine suggests
// acceptEdits is not that transition — it goes to the human, and their
// allow carries the approved default, never the wider suggestion.
func TestApprovals_ASuggestionWiderThanTheApprovedPostureIsTheHumans(t *testing.T) {
	codec, ok := claude.Claude{}.Approvals().Get()
	require.True(t, ok)
	root := &scriptedRoot{answers: []engine.PermissionAnswer{{Allow: true, SetMode: engine.Provide("default")}, {Allow: true}}}
	h := newRouteHarness(t, root.decide)
	h.a.codec = codec
	plan := `{"plan":"# Plan"}`
	h.toolUse("toolu_plan", "ExitPlanMode", plan)
	_, err := h.a.Hook(context.Background(), "PreToolUse", []byte(`{"tool_name":"ExitPlanMode","tool_input":`+plan+`,"tool_use_id":"toolu_plan"}`))
	require.NoError(t, err)

	edit := `{"file_path":"/w/a.go","old_string":"a","new_string":"b"}`
	h.toolUse("toolu_edit", "Edit", edit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.a.hold(ctx, json.RawMessage(`{"tool_name":"Edit","input":`+edit+`,"tool_use_id":"toolu_edit"}`)) }()
	raw, err := h.a.Hook(context.Background(), "PermissionRequest", []byte(`{"tool_name":"Edit","tool_input":`+edit+`,"permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"ExitPlanMode", "Edit"}, root.asked, "the edit was the human's")
	assert.JSONEq(t, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"default","destination":"session"}]}}}`, string(raw))
	assert.Equal(t, "default", h.a.heldMode())
}
