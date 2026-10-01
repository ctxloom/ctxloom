package mock_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestMockApprovalCodec_ThePlanToolIsAPlanAsk: a pre-tool ask about the
// mock's plan tool presents the plan its input carries; any other ask is a
// tool call, which may carry the engine's suggested mode change.
func TestMockApprovalCodec_ThePlanToolIsAPlanAsk(t *testing.T) {
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)

	ask, err := codec.DecodeAsk(wire.HookEventPreTool, []byte(`{"tool":"`+mock.PlanTool+`","input":{"plan":"do-it"},"tool_use_id":"p1"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.AskPlan, ask.Kind)
	require.NotNil(t, ask.Plan)
	assert.Equal(t, "do-it", ask.Plan.Markdown)
	assert.Equal(t, "p1", ask.ToolUseID)

	ask, err = codec.DecodeAsk(wire.HookEventPermissionAsk, []byte(`{"tool":"Edit","input":{},"suggests_set_mode":"default"}`))
	require.NoError(t, err)
	assert.Equal(t, engine.AskTool, ask.Kind)
	m, ok := ask.SuggestsSetMode.Get()
	assert.True(t, ok)
	assert.Equal(t, "default", m)
}

// planTurn runs one mock:plan turn at the declared mode and the turn's
// posture, with hookCommand delivered as the plan tool's pre-tool hook, and
// returns what the turn relayed.
func planTurn(t *testing.T, declared string, posture engine.TurnPosture, hookCommand string) []agent.ChatEvent {
	t.Helper()
	dir := t.TempDir()
	hooks := wire.UnifiedHooks{PreTool: []wire.Hook{{Type: "command", Command: hookCommand, Matcher: mock.PlanTool}}}
	raw, err := json.Marshal(hooks)
	require.NoError(t, err)
	hooksFile := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksFile, raw, 0o600))

	policy := engine.PermissionPolicy{Approver: engine.ApproverHuman, Posture: engine.Posture{Engine: mock.Name, Document: map[string]any{"mode": declared}}}
	inst, err := mock.New().Instance(engine.Session{Mode: engine.Structured, WorkDir: dir, Permission: policy})
	require.NoError(t, err)
	out := make(chan engine.Event, 64)
	ex := engine.Exec{Args: []string{mock.HooksFlag, hooksFile}, WorkDir: dir, Env: map[string]string{}}
	_, err = inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: mock.Plan("ship-it"), Posture: posture}, out)
	require.NoError(t, err)
	close(out)
	var evs []agent.ChatEvent
	for ev := range out {
		var ce agent.ChatEvent
		require.NoError(t, json.Unmarshal(ev.Payload, &ce))
		evs = append(evs, ce)
	}
	return evs
}

// planOutcome is the plan call's result entry and the modes the turn
// announced, in order.
func planOutcome(evs []agent.ChatEvent) (result *agent.SessionEntry, modes []string) {
	for _, ev := range evs {
		switch {
		case ev.Session != nil:
			modes = append(modes, ev.Session.PermissionMode)
		case ev.Entry != nil && ev.Entry.ToolCallID == mock.PlanCallID && ev.Entry.Type == agent.EntryTypeToolResult:
			result = ev.Entry
		}
	}
	return result, modes
}

// TestMockPlan_AnApprovedPlanLeavesPlanModeAtDefault: in plan posture the
// turn presents its plan through the pre-tool hook; an allow runs it and the
// session announces it left plan mode at default.
func TestMockPlan_AnApprovedPlanLeavesPlanModeAtDefault(t *testing.T) {
	result, modes := planOutcome(planTurn(t, "plan", engine.TurnPosture{}, `cat >/dev/null; printf '{"allow":true}'`))
	require.NotNil(t, result, "the plan call has a result")
	assert.False(t, result.IsError)
	assert.Equal(t, []string{"plan", "default"}, modes)
}

// TestMockPlan_ARejectedPlanStaysInPlanMode: a deny is the plan's result —
// the feedback the model revises from — and the mode does not change.
func TestMockPlan_ARejectedPlanStaysInPlanMode(t *testing.T) {
	result, modes := planOutcome(planTurn(t, "plan", engine.TurnPosture{}, `cat >/dev/null; printf '{"allow":false,"message":"split it"}'`))
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Equal(t, "split it", result.ToolOutput)
	assert.Equal(t, []string{"plan"}, modes)
}

// TestMockPlan_OnlyAPlanPosturePresentsAPlan: a turn whose posture is not
// plan — the declared plan moved on by the turn's own mode — makes no plan
// call.
func TestMockPlan_OnlyAPlanPosturePresentsAPlan(t *testing.T) {
	result, modes := planOutcome(planTurn(t, "plan", engine.TurnPosture{Mode: "default"}, `cat >/dev/null; printf '{"allow":true}'`))
	assert.Nil(t, result)
	assert.Equal(t, []string{"default"}, modes)
}

// TestMockApprovalCodec_APlanAnswerIsAllowOrDeny: the plan's answer carries
// the decision and the feedback, nothing else.
func TestMockApprovalCodec_APlanAnswerIsAllowOrDeny(t *testing.T) {
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	ask := engine.PermissionAsk{Kind: engine.AskPlan, Tool: mock.PlanTool}
	raw, err := codec.EncodeAnswer(wire.HookEventPreTool, ask, engine.PermissionAnswer{Allow: false, Message: "split it"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"allow":false,"message":"split it"}`, string(raw))
}
