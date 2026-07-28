package transcript

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestPayloadFromChatEvent_AllVariants pins the ChatEvent → Record payload
// conversion for each of the four variants directly, independent of the
// Recorder's file I/O — the schema.go half of this package in isolation.
func TestPayloadFromChatEvent_AllVariants(t *testing.T) {
	t.Run("entry", func(t *testing.T) {
		ev := agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:       agent.EntryTypeToolUse,
			Content:    "",
			ToolName:   "read",
			ToolInput:  json.RawMessage(`{"path":"probe6.txt"}`),
			ToolOutput: "",
			IsError:    false,
			Sidechain:  true,
		}}
		kind, entry, session, complete, permission, err := payloadFromChatEvent(ev)
		require.NoError(t, err)
		assert.Equal(t, KindEntry, kind)
		assert.Nil(t, session)
		assert.Nil(t, complete)
		assert.Nil(t, permission)
		require.NotNil(t, entry)
		assert.Equal(t, "tool_use", entry.Type)
		assert.Equal(t, "read", entry.ToolName)
		assert.JSONEq(t, `{"path":"probe6.txt"}`, string(entry.ToolInput))
		assert.True(t, entry.Sidechain)
	})

	t.Run("session", func(t *testing.T) {
		ev := agent.ChatEvent{Session: &agent.ChatSessionInfo{
			SessionID:      "sess-1",
			Model:          "gpt-5.4-mini",
			PermissionMode: "default",
			ContextWindow:  258400,
			MCPServers:     []agent.MCPStatus{{Name: "ctxloom", Status: "connected"}},
		}}
		kind, entry, session, complete, permission, err := payloadFromChatEvent(ev)
		require.NoError(t, err)
		assert.Equal(t, KindSession, kind)
		assert.Nil(t, entry)
		assert.Nil(t, complete)
		assert.Nil(t, permission)
		require.NotNil(t, session)
		assert.Equal(t, "gpt-5.4-mini", session.Model)
		assert.Equal(t, 258400, session.ContextWindow)
		require.Len(t, session.MCPServers, 1)
		assert.Equal(t, "ctxloom", session.MCPServers[0].Name)
		assert.Equal(t, "sess-1", ev.Session.SessionID)
	})

	t.Run("complete", func(t *testing.T) {
		ev := agent.ChatEvent{Complete: &agent.TurnMeta{
			InputTokens:     20240,
			OutputTokens:    27,
			CacheReadTokens: 4480,
			ContextWindow:   258400,
			Model:           "gpt-5.4-mini",
			StopReason:      "end_turn",
		}}
		kind, entry, session, complete, permission, err := payloadFromChatEvent(ev)
		require.NoError(t, err)
		assert.Equal(t, KindComplete, kind)
		assert.Nil(t, entry)
		assert.Nil(t, session)
		assert.Nil(t, permission)
		require.NotNil(t, complete)
		assert.Equal(t, 20240, complete.InputTokens)
		assert.Equal(t, 4480, complete.CacheReadTokens)
		assert.Equal(t, "end_turn", complete.StopReason)
	})

	t.Run("permission", func(t *testing.T) {
		ev := agent.ChatEvent{Permission: &agent.PermissionRequest{
			ID:        "perm-1",
			ToolName:  "rm",
			ToolInput: json.RawMessage(`{"path":"/tmp/scratch"}`),
			Kind:      "delete",
			Options: []agent.PermissionOption{
				{ID: "allow_once", Kind: "allow_once", Name: "Allow once"},
			},
		}}
		kind, entry, session, complete, permission, err := payloadFromChatEvent(ev)
		require.NoError(t, err)
		assert.Equal(t, KindPermission, kind)
		assert.Nil(t, entry)
		assert.Nil(t, session)
		assert.Nil(t, complete)
		require.NotNil(t, permission)
		assert.Equal(t, "perm-1", permission.ID)
		assert.Equal(t, "delete", permission.Kind)
		require.Len(t, permission.Options, 1)
		assert.Equal(t, "allow_once", permission.Options[0].ID)
	})

	t.Run("empty ChatEvent is rejected, not silently recorded blank", func(t *testing.T) {
		_, _, _, _, _, err := payloadFromChatEvent(agent.ChatEvent{})
		assert.Error(t, err)
	})
}

// TestRecord_JSONRoundTrip pins the wire shape: marshal a fully-populated
// Record, unmarshal it back, and assert every field survives — the exact
// discipline the project's broken readers skipped (asserting shape, not just
// "no error").
func TestRecord_JSONRoundTrip(t *testing.T) {
	original := Record{
		V:         SchemaVersion,
		Harp:      "sixth-moist-kite",
		SessionID: "019f6226-e5d2-75f3-b8bb-667866092679",
		Engine:    "codex",
		Seq:       3,
		Kind:      KindEntry,
		Entry: &EntryPayload{
			Type:      "tool_use",
			ToolName:  "shell",
			ToolInput: json.RawMessage(`{"command":["cat","probe.txt"]}`),
		},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded Record
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, original.V, decoded.V)
	assert.Equal(t, original.Harp, decoded.Harp)
	assert.Equal(t, original.SessionID, decoded.SessionID)
	assert.Equal(t, original.Engine, decoded.Engine)
	assert.Equal(t, original.Seq, decoded.Seq)
	assert.Equal(t, original.Kind, decoded.Kind)
	require.NotNil(t, decoded.Entry)
	assert.Equal(t, "shell", decoded.Entry.ToolName)
	assert.JSONEq(t, `{"command":["cat","probe.txt"]}`, string(decoded.Entry.ToolInput))
	assert.Nil(t, decoded.Session)
	assert.Nil(t, decoded.Complete)
	assert.Nil(t, decoded.Permission)
}
