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
		evs := stepEvent("USER_INPUT", "<USER_REQUEST>\n\n</USER_REQUEST>")
		assert.Empty(t, evs, "an empty extracted user request contributes nothing")
	})

	t.Run("PLANNER_RESPONSE with empty content yields nothing", func(t *testing.T) {
		evs := stepEvent("PLANNER_RESPONSE", "   ")
		assert.Empty(t, evs, "a blank assistant response contributes nothing")
	})

	t.Run("USER_INPUT maps to a user entry", func(t *testing.T) {
		evs := stepEvent("USER_INPUT", "<USER_REQUEST>\nhello\n</USER_REQUEST>")
		require.Len(t, evs, 1)
		require.NotNil(t, evs[0].Entry)
		assert.Equal(t, agent.EntryTypeUser, evs[0].Entry.Type)
		assert.Equal(t, "hello", evs[0].Entry.Content)
	})

	t.Run("PLANNER_RESPONSE maps to an assistant entry", func(t *testing.T) {
		evs := stepEvent("PLANNER_RESPONSE", "ok")
		require.Len(t, evs, 1)
		require.NotNil(t, evs[0].Entry)
		assert.Equal(t, agent.EntryTypeAssistant, evs[0].Entry.Type)
		assert.Equal(t, "ok", evs[0].Entry.Content)
	})

	// ERROR_MESSAGE is deliberately NOT in this list: it is mapped, to
	// entry.type "system". Every type below is administrative or
	// tool narration whose omission the package doc justifies; a failure
	// notice never belonged with them.
	t.Run("an unmapped type contributes nothing, even with content", func(t *testing.T) {
		for _, typ := range []string{
			"CONVERSATION_HISTORY", "CHECKPOINT", "GENERIC",
			"LIST_DIRECTORY", "RUN_COMMAND", "CODE_ACTION", "VIEW_FILE",
			"SYSTEM_MESSAGE", "SOME_FUTURE_TYPE",
		} {
			evs := stepEvent(typ, "some narration text")
			assert.Empty(t, evs, "type %q must not be converted", typ)
		}
	})
}

// TestStepEvent_ErrorMessageBecomesASystemEntry pins that an
// ERROR_MESSAGE step is antigravity's own record that something in the
// session FAILED. Dropping it silently makes a failed session import as a
// clean transcript with no trace of the failure anywhere — this project's
// signature bug shape, a success carrying zero evidence. No vocabulary is
// invented to fix it: agent.EntryTypeSystem with the default
// SystemKindNotice ("a freeform system notice with no structured payload")
// is exactly the slot, and the schema's entry.type enum already lists it.
func TestStepEvent_ErrorMessageBecomesASystemEntry(t *testing.T) {
	evs := stepEvent("ERROR_MESSAGE", "  Tool call failed: exit status 1  ")
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeSystem, evs[0].Entry.Type)
	assert.Equal(t, agent.SystemKindNotice, evs[0].Entry.SystemKind)
	assert.Equal(t, "Tool call failed: exit status 1", evs[0].Entry.Content)
}

// An ERROR_MESSAGE step with nothing in it is still nothing to record —
// TextEntry's "zero or one" discipline, same as every other mapped type.
func TestStepEvent_EmptyErrorMessageYieldsNothing(t *testing.T) {
	assert.Empty(t, stepEvent("ERROR_MESSAGE", "   "))
}
