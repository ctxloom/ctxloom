// Pure-function table tests for the response_item sub-variant mappers that
// codex_test.go's real-shaped fixture cannot exercise: reasoning.summary was
// EMPTY on every rollout captured for that fixture (see
// testdata/MANIFEST.json), so its non-empty path is covered here instead,
// against a SYNTHETIC payload shaped from codex's documented envelope, not a
// real capture — same "unverified shape" caveat rollout.go's
// reasoningEvents doc comment carries. These tests need no testdata/ fixture
// and no Recorder; they call the mapping functions directly.
package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestReasoningEvents(t *testing.T) {
	t.Run("empty summary yields nothing", func(t *testing.T) {
		evs := reasoningEvents(responseItemPayload{Type: "reasoning"})
		assert.Nil(t, evs)
	})

	t.Run("string-element summary becomes a thinking entry", func(t *testing.T) {
		var summary []json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(`["first thought", "second thought"]`), &summary))
		evs := reasoningEvents(responseItemPayload{Type: "reasoning", Summary: summary})
		require.Len(t, evs, 1)
		require.NotNil(t, evs[0].Entry)
		assert.Equal(t, agent.EntryTypeThinking, evs[0].Entry.Type)
		assert.Equal(t, "first thought\n\nsecond thought", evs[0].Entry.Content)
	})

	t.Run("object-element summary ({text:...}) becomes a thinking entry", func(t *testing.T) {
		var summary []json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(`[{"type":"summary_text","text":"weighing the options"}]`), &summary))
		evs := reasoningEvents(responseItemPayload{Type: "reasoning", Summary: summary})
		require.Len(t, evs, 1)
		assert.Equal(t, "weighing the options", evs[0].Entry.Content)
	})
}

func TestMessageEvents_DeveloperRoleSkipped(t *testing.T) {
	evs := messageEvents(responseItemPayload{
		Type:    "message",
		Role:    "developer",
		Content: []contentBlock{{Type: "input_text", Text: "system instructions the human never typed"}},
	})
	assert.Nil(t, evs, "developer role must never become a canonical entry")
}

func TestMessageEvents_EmptyContentSkipped(t *testing.T) {
	evs := messageEvents(responseItemPayload{Type: "message", Role: "user", Content: nil})
	assert.Nil(t, evs, "a role=user message with no text content contributes nothing")
}

func TestArgumentsToRaw(t *testing.T) {
	t.Run("valid JSON string passes through as-is", func(t *testing.T) {
		raw := argumentsToRaw(`{"cmd":"ls"}`)
		assert.JSONEq(t, `{"cmd":"ls"}`, string(raw))
	})

	t.Run("empty arguments yields nil", func(t *testing.T) {
		assert.Nil(t, argumentsToRaw(""))
	})

	t.Run("non-JSON arguments is wrapped as a JSON string literal, never raw bytes", func(t *testing.T) {
		raw := argumentsToRaw("not json at all")
		var s string
		require.NoError(t, json.Unmarshal(raw, &s), "must decode as a valid JSON string")
		assert.Equal(t, "not json at all", s)
	})
}

func TestFunctionCallOutputEvents_NeverGuessesIsError(t *testing.T) {
	evs := functionCallOutputEvents(responseItemPayload{
		CallID: "call_1",
		Output: "Process exited with code 1\nsomething failed",
	})
	require.Len(t, evs, 1)
	assert.False(t, evs[0].Entry.IsError, "codex's function_call_output carries no explicit error field; a nonzero exit code in free text must not be sniffed into a fabricated boolean")
}

// TestScanSessionInfo_FallsBackToSessionIDOnOlderBuilds pins that older
// codex CLI builds' session_meta payload carries the session id ONLY under
// the legacy "session_id" key (never observed with an empty "id" AND no
// "session_id" on this box, but the fallback exists specifically because a
// future/older codex build could omit "id"). Absence from today's fixture
// corpus is not evidence this branch is dead — it is untested vendor-format
// defensive handling, exactly the shape this project's review discipline
// says to keep and cover, not delete.
func TestScanSessionInfo_FallsBackToSessionIDOnOlderBuilds(t *testing.T) {
	line := []byte(`{"type":"session_meta","payload":{"session_id":"legacy-only-id"}}`)
	info, err := scanSessionInfo(context.Background(), [][]byte{line})
	require.NoError(t, err)
	require.NotNil(t, info, "a session_meta line, even id-less, must latch SOME session info")
	assert.Equal(t, "legacy-only-id", info.SessionID, "id-less session_meta must fall back to the legacy session_id key")
}
