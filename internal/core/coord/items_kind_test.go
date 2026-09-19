package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestItemKind_CoversEveryPayloadCase pins the whole classification: which
// AgentEvent payloads are journaled item facts and what each one is named. The
// names are also the journal's own vocabulary (they land in items.jsonl and are
// counted by kind on replay), so this is the fold's key contract as much as a
// dispatch table.
//
// Kinds with their own durability path — custom, summary, artifact_produced —
// and an event with no payload at all classify as "", which is what routes them
// away from item journaling.
func TestItemKind_CoversEveryPayloadCase(t *testing.T) {
	cases := []struct {
		want string
		ev   *agentcoordpb.AgentEvent
	}{
		{"run_started", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunStarted{RunStarted: &agentcoordpb.RunStarted{}}}},
		{"step_started", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_StepStarted{StepStarted: &agentcoordpb.StepStarted{}}}},
		{"step_completed", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_StepCompleted{StepCompleted: &agentcoordpb.StepCompleted{}}}},
		{"status_changed", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_StatusChanged{StatusChanged: &agentcoordpb.StatusChanged{}}}},
		{"run_completed", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}}},
		{"message_started", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{}}}},
		{"message_delta", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{}}}},
		{"message_completed", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{}}}},
		{"tool_call_started", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{}}}},
		{"tool_call_args_delta", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallArgsDelta{ToolCallArgsDelta: &agentcoordpb.ToolCallArgsDelta{}}}},
		{"tool_call_completed", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentcoordpb.ToolCallCompleted{}}}},
		{"interaction", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Interaction{Interaction: &agentcoordpb.InteractionRecorded{}}}},
		{"raw", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Raw{Raw: &agentcoordpb.RawEvent{}}}},
		{"", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{}}}},
		{"", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Summary{Summary: &agentcoordpb.Summary{}}}},
		{"", &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_ArtifactProduced{ArtifactProduced: &agentcoordpb.ArtifactProduced{}}}},
		{"", &agentcoordpb.AgentEvent{}},
		{"", nil},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, itemKind(tc.ev), "payload %T", tc.ev.GetPayload())
	}
}

// TestItemKind_DeltaAndBoundaryAgree: the group-fsync policy reads the kind
// string, so the two delta kinds must be exactly the two the flush policy
// treats as storm traffic — every other kind is a flush boundary.
func TestItemKind_DeltaAndBoundaryAgree(t *testing.T) {
	assert.True(t, itemIsDelta("message_delta"))
	assert.True(t, itemIsDelta("tool_call_args_delta"))
	for _, boundary := range []string{"run_started", "run_completed", "message_completed", "tool_call_completed", "interaction", "raw", "status_changed"} {
		assert.False(t, itemIsDelta(boundary), "%s must force a flush", boundary)
	}
}
