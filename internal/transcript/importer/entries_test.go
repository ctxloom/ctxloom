package importer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestNonEmptyRaw(t *testing.T) {
	assert.Nil(t, nonEmptyRaw(nil))
	assert.Nil(t, nonEmptyRaw(json.RawMessage{}))
	assert.Equal(t, json.RawMessage(`{"a":1}`), nonEmptyRaw(json.RawMessage(`{"a":1}`)))
}

func TestTextEntry(t *testing.T) {
	assert.Nil(t, TextEntry(agent.EntryTypeUser, ""))

	evs := TextEntry(agent.EntryTypeAssistant, "hi")
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[0].Entry.Type)
	assert.Equal(t, "hi", evs[0].Entry.Content)
}

func TestToolUseEvent(t *testing.T) {
	ev := ToolUseEvent("Grep", "call-1", json.RawMessage(`{"pattern":"x"}`))
	require.NotNil(t, ev.Entry)
	assert.Equal(t, agent.EntryTypeToolUse, ev.Entry.Type)
	assert.Equal(t, "Grep", ev.Entry.ToolName)
	assert.Equal(t, "call-1", ev.Entry.ToolCallID)
	assert.Equal(t, json.RawMessage(`{"pattern":"x"}`), ev.Entry.ToolInput)

	// Empty input normalizes to nil (omitempty-safe), never an empty-but-non-nil slice.
	ev = ToolUseEvent("Grep", "call-1", json.RawMessage(""))
	assert.Nil(t, ev.Entry.ToolInput)
}

func TestToolResultEvent(t *testing.T) {
	ev := ToolResultEvent("call-1", "output text", true, nil)
	require.NotNil(t, ev.Entry)
	assert.Equal(t, agent.EntryTypeToolResult, ev.Entry.Type)
	assert.Equal(t, "call-1", ev.Entry.ToolCallID)
	assert.Equal(t, "output text", ev.Entry.ToolOutput)
	assert.True(t, ev.Entry.IsError)
}
