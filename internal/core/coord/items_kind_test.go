package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		ev   Event
	}{
		{"run_started", Event{Payload: RunStarted{}}},
		{"step_started", Event{Payload: StepStarted{}}},
		{"step_completed", Event{Payload: StepCompleted{}}},
		{"status_changed", Event{Payload: StatusChanged{}}},
		{"run_completed", Event{Payload: RunCompleted{}}},
		{"message_started", Event{Payload: MessageStarted{}}},
		{"message_delta", Event{Payload: MessageDelta{}}},
		{"message_completed", Event{Payload: MessageCompleted{}}},
		{"tool_call_started", Event{Payload: ToolCallStarted{}}},
		{"tool_call_args_delta", Event{Payload: ToolCallArgsDelta{}}},
		{"tool_call_completed", Event{Payload: ToolCallCompleted{}}},
		{"interaction", Event{Payload: InteractionRecorded{}}},
		{"raw", Event{Payload: RawEvent{}}},
		{"", Event{Payload: CustomEvent{}}},
		{"", Event{Payload: Summary{}}},
		{"", Event{Payload: ArtifactProduced{}}},
		{"", Event{Payload: EventsLost{}}},
		{"", Event{}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, itemKind(tc.ev), "payload %T", tc.ev.Payload)
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
