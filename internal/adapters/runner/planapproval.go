package runner

import (
	"context"

	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// A run that plans first (engine.PermissionModel.PlansFirst) is approved
// BETWEEN turns: each turn ends holding a plan, its automatic report carries
// that plan as data (coord.PlanApproval), and the parent approves it by
// sending the child a message whose structured companion names the posture
// under approvePlanKey. The turn that message starts is handed
// planApprovedPrompt instead of the message, at the approved posture, and
// every later turn of the run starts there. The approval is the run's
// alone: a relaunch plans again.

// approvePlanKey is the structured key a parent's message approves the
// child's plan by; its value is one of the offered postures, "" for the
// default.
const approvePlanKey = "approve_plan"

// planApprovedPrompt is the approved turn's prompt.
const planApprovedPrompt = "Your plan was approved. Carry it out now. You are running at the permission mode it was approved for."

// planApprovalIn is the posture a delivered message approves the plan at;
// absent when it approves nothing. Only text names a posture.
func planApprovalIn(pm *agentcoordpb.PeerMessage) engine.Declared[string] {
	v, ok := pm.GetStructured().GetFields()[approvePlanKey]
	if !ok {
		return engine.Declared[string]{}
	}
	s, isText := v.GetKind().(*structpb.Value_StringValue)
	if !isText {
		return engine.Declared[string]{}
	}
	return engine.Provide(s.StringValue)
}

// SetPlanStamp binds what publishes the plan a turn left as an artifact and
// names it ("" for none): the session endpoint's artifact stamper, which
// alone knows where the session's plans are. One per Home, like the turn
// sink: a second binding is a wiring bug and is refused.
func (h *Home) SetPlanStamp(stamp func(ctx context.Context) string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.planStamp != nil {
		h.rep.Warnf("runner: a plan stamp is already bound for this run; the second binding is refused")
		return
	}
	h.planStamp = stamp
}

// stampPlan runs the bound plan stamp; "" with none bound.
func (h *Home) stampPlan(ctx context.Context) string {
	h.mu.Lock()
	stamp := h.planStamp
	h.mu.Unlock()
	if stamp == nil {
		return ""
	}
	return stamp(ctx)
}
