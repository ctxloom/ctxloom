package acp

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/acp/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// mapUpdateJSON wraps a raw `update` object in a session/update notification and
// runs it through the real decode+map path — the same one the live read loop
// uses — so these tests exercise wire decoding, not just in-memory structs.
func mapUpdateJSON(t *testing.T, updateJSON string) []agent.ChatEvent {
	t.Helper()
	params := []byte(`{"sessionId":"s1","update":` + updateJSON + `}`)
	upd, err := decodeSessionUpdate(params)
	require.NoError(t, err)
	return mapSessionUpdate(upd)
}

// oneEntry asserts the events are exactly one Entry event and returns it.
func oneEntry(t *testing.T, evs []agent.ChatEvent) *agent.SessionEntry {
	t.Helper()
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	return evs[0].Entry
}

// TestMapSessionUpdate_TextAndThinking pins the headline mapping: assistant text
// and — the win — summarized reasoning as EntryTypeThinking.
func TestMapSessionUpdate_TextAndThinking(t *testing.T) {
	t.Run("agent_message_chunk → assistant", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello world"}}`))
		assert.Equal(t, agent.EntryTypeAssistant, e.Type)
		assert.Equal(t, "hello world", e.Content)
	})

	t.Run("agent_thought_chunk → thinking", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"let me reason"}}`))
		assert.Equal(t, agent.EntryTypeThinking, e.Type)
		assert.Equal(t, "let me reason", e.Content)
	})

	t.Run("empty text chunk is dropped", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}`))
	})

	t.Run("non-text content block is dropped", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"aGk=","mimeType":"image/png"}}`))
	})
}

// TestMapSessionUpdate_ToolCall pins tool_call → tool_use (title→name, rawInput→input).
func TestMapSessionUpdate_ToolCall(t *testing.T) {
	t.Run("title + rawInput", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call","toolCallId":"tc1","title":"Read file","kind":"read","status":"pending","rawInput":{"path":"/etc/hosts"}}`))
		assert.Equal(t, agent.EntryTypeToolUse, e.Type)
		assert.Equal(t, "Read file", e.ToolName)
		assert.JSONEq(t, `{"path":"/etc/hosts"}`, string(e.ToolInput))
	})

	t.Run("falls back to kind when no title", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call","toolCallId":"tc2","kind":"execute","status":"pending"}`))
		assert.Equal(t, agent.EntryTypeToolUse, e.Type)
		assert.Equal(t, "execute", e.ToolName)
	})
}

// TestMapSessionUpdate_ToolCallUpdate pins tool_call_update → tool_result, only
// once it carries output or a terminal status; failure marks IsError.
func TestMapSessionUpdate_ToolCallUpdate(t *testing.T) {
	t.Run("completed with content → tool_result", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"file body"}}]}`))
		assert.Equal(t, agent.EntryTypeToolResult, e.Type)
		assert.Equal(t, "file body", e.ToolOutput)
		assert.False(t, e.IsError)
	})

	t.Run("failed with rawOutput → error tool_result", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"failed","rawOutput":"boom"}`))
		assert.Equal(t, agent.EntryTypeToolResult, e.Type)
		assert.Equal(t, "boom", e.ToolOutput)
		assert.True(t, e.IsError)
	})

	t.Run("diff content is flattened", func(t *testing.T) {
		e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"completed","content":[{"type":"diff","path":"/a.txt","newText":"new"}]}`))
		assert.Contains(t, e.ToolOutput, "/a.txt")
		assert.Contains(t, e.ToolOutput, "new")
	})

	t.Run("bare in-progress tick yields nothing", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"in_progress"}`))
	})
}

// TestMapSessionUpdate_Plan pins plan → a system checklist entry.
func TestMapSessionUpdate_Plan(t *testing.T) {
	e := oneEntry(t, mapUpdateJSON(t, `{"sessionUpdate":"plan","entries":[{"content":"Do X","priority":"high","status":"pending"},{"content":"Do Y","priority":"medium","status":"in_progress"}]}`))
	assert.Equal(t, agent.EntryTypeSystem, e.Type)
	assert.Contains(t, e.Content, "[pending] Do X")
	assert.Contains(t, e.Content, "[in_progress] Do Y")
}

// TestMapSessionUpdate_Dropped pins the variants and shapes that must yield no
// entries (never crash, never duplicate the user's own message).
func TestMapSessionUpdate_Dropped(t *testing.T) {
	t.Run("user_message_chunk is not echoed back", func(t *testing.T) {
		assert.Empty(t, mapUpdateJSON(t, `{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"my prompt"}}`))
	})

	t.Run("nil update", func(t *testing.T) {
		assert.Empty(t, mapSessionUpdate(nil))
	})

	t.Run("unknown sessionUpdate variant fails to decode (warned, dropped)", func(t *testing.T) {
		// The union rejects an unmodeled discriminator; the live handler warns and
		// drops rather than crashing the stream.
		_, err := decodeSessionUpdate([]byte(`{"sessionId":"s","update":{"sessionUpdate":"quantum_flux"}}`))
		assert.Error(t, err)
	})
}

// TestDecidePermission pins the allow/deny decision (mirrors the claude driver:
// allow only under a bypass posture) and the option-kind preference.
func TestDecidePermission(t *testing.T) {
	full := []api.PermissionOption{
		{Kind: api.PermissionOptionKindAllowAlways, Name: "Always allow", OptionId: "aa"},
		{Kind: api.PermissionOptionKindAllowOnce, Name: "Allow once", OptionId: "ao"},
		{Kind: api.PermissionOptionKindRejectOnce, Name: "Reject once", OptionId: "ro"},
		{Kind: api.PermissionOptionKindRejectAlways, Name: "Reject always", OptionId: "ra"},
	}

	t.Run("allow selects allow_once (preferred over allow_always)", func(t *testing.T) {
		got := decidePermission(full, true)
		assert.Equal(t, outcomeSelected, got.Outcome.Outcome)
		assert.Equal(t, "ao", got.Outcome.OptionId)
	})

	t.Run("deny selects reject_once", func(t *testing.T) {
		got := decidePermission(full, false)
		assert.Equal(t, outcomeSelected, got.Outcome.Outcome)
		assert.Equal(t, "ro", got.Outcome.OptionId)
	})

	t.Run("deny with no reject option falls back to cancelled", func(t *testing.T) {
		got := decidePermission([]api.PermissionOption{{Kind: api.PermissionOptionKindAllowOnce, OptionId: "ao"}}, false)
		assert.Equal(t, outcomeCancelled, got.Outcome.Outcome)
		assert.Empty(t, got.Outcome.OptionId)
	})

	t.Run("allow with no options falls back to cancelled", func(t *testing.T) {
		got := decidePermission(nil, true)
		assert.Equal(t, outcomeCancelled, got.Outcome.Outcome)
	})
}
