package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// heldPosture is the posture the tests move a run to: the mock's one
// transition.
const heldPosture = "default"

// askAbout has the route decide a call the id names; suggests is the mode
// change the engine offers with it ("" for none).
func askAbout(t *testing.T, h *routeHarness, id, tool, suggests string) mockAnswer {
	t.Helper()
	h.toolUse(id, tool, `{}`)
	raw, err := h.a.Hook(context.Background(), wire.HookEventPermissionAsk, []byte(`{"tool":"`+tool+`","input":{},"suggests_set_mode":"`+suggests+`"}`))
	require.NoError(t, err)
	return decodeAnswer(t, raw)
}

// scriptedRoot answers asks in order from answers.
type scriptedRoot struct{ answers []engine.PermissionAnswer }

func (r *scriptedRoot) decide(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
	ans := r.answers[0]
	r.answers = r.answers[1:]
	return ans, nil
}

// TestApprovals_AnAllowsModeChangeIsHeld: a mode change the human allows for
// a call — the engine's own suggestion, granted for the session — reaches
// the engine with the allow and is the run's posture from then on. A deny
// moves nothing.
func TestApprovals_AnAllowsModeChangeIsHeld(t *testing.T) {
	root := &scriptedRoot{answers: []engine.PermissionAnswer{{Allow: false, Message: "no"}, {Allow: true, SetMode: engine.Provide(heldPosture)}}}
	h := newRouteHarness(t, root.decide)
	assert.Equal(t, mockAnswer{Message: "no"}, askAbout(t, h, "e1", "Edit", heldPosture))
	assert.Empty(t, h.a.heldMode(), "a deny moves nothing")
	assert.Equal(t, mockAnswer{Allow: true, SetMode: heldPosture}, askAbout(t, h, "e2", "Edit", heldPosture))
	assert.Equal(t, heldPosture, h.a.heldMode())
	h.a.endTurn()
	assert.Equal(t, heldPosture, h.a.heldMode(), "the posture outlives the turn that moved it")
}

// TestEngineHost_AnAllowedModeChangeStartsEveryLaterTurn: the engine's mode
// does not survive its process, so the posture an allow moved the run to
// reaches each later turn as the turn's own mode — never the turn that
// asked, which was handed its posture when it started.
func TestEngineHost_AnAllowedModeChangeStartsEveryLaterTurn(t *testing.T) {
	eh, eng := drivePostures(t, true)
	eng.home.mu.Lock()
	eng.home.requestFn = func(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		d := &agentcoordpb.ApprovalDecision{Allow: true, Decider: "human"}
		if req.GetApproval().GetSuggestsSetMode() != "" {
			d.SetMode = req.GetApproval().GetSuggestsSetMode()
		}
		return &agentcoordpb.CoordinatorResponse{Status: coordgrpc.OKStatus(""), Kind: &agentcoordpb.CoordinatorResponse_Approval{Approval: d}}, nil
	}
	eng.home.mu.Unlock()
	nextPosture(t, eng) // the briefing: an ask, allowed with no mode change

	resp := eh.Handle(turnReq("edit"))
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Empty(t, nextPosture(t, eng).Mode, "the asking turn runs at the declared posture")

	for range 2 {
		resp = eh.Handle(turnReq("plain"))
		require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		assert.Equal(t, heldPosture, nextPosture(t, eng).Mode)
	}
}
