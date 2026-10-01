package mock

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// A mock:plan turn, in plan posture, presents a plan the way claude does:
// the plan tool's call goes out on the stream and the delivered pre-tool
// hooks for that tool decide it. An allow runs it and the session leaves
// plan mode at default (announced as a session frame); a deny is the call's
// result — the feedback the model revises from — and the mode stays plan.
// Outside plan posture there is no plan to present, and the turn makes no
// call.

// planPattern is the marker: `mock:plan=<plan text, no spaces>`.
var planPattern = regexp.MustCompile(`mock:plan=(\S+)`)

// Plan renders the prompt directive that makes the mock's turn present plan.
func Plan(plan string) string { return "mock:plan=" + plan }

// PlanTool is the mock's plan tool: a pre-tool ask about it is a plan.
const PlanTool = "exit_plan_mode"

// PlanCallID is the id of the call a mock:plan turn makes.
const PlanCallID = "mock-plan-1"

// modePlan is the mock posture that presents plans; modeAfterPlan the one an
// approved plan leaves it at.
const (
	modePlan      = "plan"
	modeAfterPlan = "default"
)

// planIn reads the plan a prompt asks the turn to present.
func planIn(prompt string) (string, bool) {
	m := planPattern.FindStringSubmatch(prompt)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// planInput is the plan tool's input.
type planInput struct {
	Plan string `json:"plan"`
}

// presentPlan makes the plan call and has the plan tool's pre-tool hooks
// decide it; with no hook answering, nobody approved it.
func (d driver) presentPlan(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, send func(agent.ChatEvent) error, plan string) error {
	input, err := json.Marshal(planInput{Plan: plan})
	if err != nil {
		return err
	}
	if err := send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: PlanTool, ToolCallID: PlanCallID, ToolInput: input}}); err != nil {
		return err
	}
	payload, err := json.Marshal(mockCall{Tool: PlanTool, Input: input, ToolUseID: PlanCallID})
	if err != nil {
		return err
	}
	dec, answered, err := firstHookAnswer(ctx, ex, hooks.PreTool, PlanTool, payload)
	if err != nil {
		return err
	}
	if !answered {
		dec = askDecision{Message: "mock: nobody answered the plan"}
	}
	result := agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: PlanCallID, ToolOutput: "mock: plan approved"}
	if !dec.Allow {
		result.ToolOutput, result.IsError = dec.Message, true
	}
	if err := send(agent.ChatEvent{Entry: &result}); err != nil || !dec.Allow {
		return err
	}
	return send(agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: sessionKey, Resumable: true, PermissionMode: modeAfterPlan}})
}
