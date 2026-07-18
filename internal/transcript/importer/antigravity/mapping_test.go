// Pure-function table tests for extractUserRequest/stepEvent — the edge
// cases antigravity_test.go's real-shaped fixture doesn't happen to exercise
// (a wrapper-less USER_INPUT, an empty PLANNER_RESPONSE, an unrecognized
// type). These tests need no testdata/ fixture and no Recorder; they call
// the mapping functions directly, mirroring codex's mapping_test.go.
package antigravity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestExtractUserRequest(t *testing.T) {
	t.Run("strips ADDITIONAL_METADATA/USER_SETTINGS_CHANGE siblings", func(t *testing.T) {
		content := "<USER_REQUEST>\nReply with just: ok\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-06-10T17:55:25-05:00.\n</ADDITIONAL_METADATA>"
		assert.Equal(t, "Reply with just: ok", extractUserRequest(content))
	})

	t.Run("no wrapper falls back to the full trimmed content", func(t *testing.T) {
		assert.Equal(t, "bare prompt, no wrapper", extractUserRequest("  bare prompt, no wrapper  "))
	})

	t.Run("unterminated wrapper falls back to the full trimmed content", func(t *testing.T) {
		content := "<USER_REQUEST>\nnever closed"
		assert.Equal(t, content, extractUserRequest(content))
	})

	t.Run("empty content yields empty", func(t *testing.T) {
		assert.Equal(t, "", extractUserRequest(""))
	})
}

func TestStepEvent(t *testing.T) {
	t.Run("USER_INPUT with only wrapper noise yields nothing", func(t *testing.T) {
		_, ok := stepEvent(step{Type: "USER_INPUT", Content: "<USER_REQUEST>\n\n</USER_REQUEST>"})
		assert.False(t, ok, "an empty extracted user request contributes nothing")
	})

	t.Run("PLANNER_RESPONSE with empty content yields nothing", func(t *testing.T) {
		_, ok := stepEvent(step{Type: "PLANNER_RESPONSE", Content: "   "})
		assert.False(t, ok, "a blank assistant response contributes nothing")
	})

	t.Run("USER_INPUT maps to a user entry", func(t *testing.T) {
		ev, ok := stepEvent(step{Type: "USER_INPUT", Content: "<USER_REQUEST>\nhello\n</USER_REQUEST>"})
		require.True(t, ok)
		require.NotNil(t, ev.Entry)
		assert.Equal(t, agent.EntryTypeUser, ev.Entry.Type)
		assert.Equal(t, "hello", ev.Entry.Content)
	})

	t.Run("PLANNER_RESPONSE maps to an assistant entry", func(t *testing.T) {
		ev, ok := stepEvent(step{Type: "PLANNER_RESPONSE", Content: "ok"})
		require.True(t, ok)
		require.NotNil(t, ev.Entry)
		assert.Equal(t, agent.EntryTypeAssistant, ev.Entry.Type)
		assert.Equal(t, "ok", ev.Entry.Content)
	})

	t.Run("an unmapped type contributes nothing, even with content", func(t *testing.T) {
		for _, typ := range []string{
			"CONVERSATION_HISTORY", "CHECKPOINT", "GENERIC",
			"LIST_DIRECTORY", "RUN_COMMAND", "CODE_ACTION", "VIEW_FILE",
			"SYSTEM_MESSAGE", "ERROR_MESSAGE", "SOME_FUTURE_TYPE",
		} {
			_, ok := stepEvent(step{Type: typ, Content: "some narration text"})
			assert.False(t, ok, "type %q must not be converted", typ)
		}
	})
}
