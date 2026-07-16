package acpagent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestMapEvent_SystemEntryEmitsVisibleFrame covers the Q1 bug: an ACP `plan`
// update lands in the IR as EntryTypeSystem (internal/acp/mapping.go's
// mapPlan renders the plan entries into a single Content string — no
// structured plan survives), and outbound mapping used to hit the
// `default: nil` case and drop it entirely. The editor would receive zero
// bytes for a plan the engine actually sent. This asserts the actual emitted
// frame's shape/bytes, not merely that mapEvent returned without error — a
// no-op mapEvent would still "succeed" by that weaker bar.
func TestMapEvent_SystemEntryEmitsVisibleFrame(t *testing.T) {
	sess := &session{}
	planText := "Plan:\n- [pending] Read the file\n- [in_progress] Apply the fix"
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeSystem,
		Content: planText,
	}}

	updates := sess.mapEvent(ev)

	require.Len(t, updates, 1, "a non-empty system entry must produce exactly one outbound frame")
	u := updates[0]
	require.NotNil(t, u.AgentMessageChunk, "agent_message_chunk payload must be present")
	require.NotNil(t, u.AgentMessageChunk.Content.Text)
	require.Equal(t, planText, u.AgentMessageChunk.Content.Text.Text,
		"the plan's rendered text must survive byte-for-byte into the outbound chunk")
}

// TestMapEvent_SystemEntryEmptyContentDropped guards the empty-content edge:
// an empty system entry (there is currently no producer of one, but the
// contract should stay honest) still yields no frame rather than an empty
// visible chunk.
func TestMapEvent_SystemEntryEmptyContentDropped(t *testing.T) {
	sess := &session{}
	ev := agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeSystem, Content: ""}}

	updates := sess.mapEvent(ev)

	require.Nil(t, updates)
}
