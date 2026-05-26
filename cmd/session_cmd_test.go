package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// TestSessionContainsHarpName exercises the harp-name match used by
// `ctxloom session distill <harp>` when no session_id was forward-
// bound. The harp name is guaranteed to appear in the LLM's recorded
// transcript because we inject it via ServerOptions.Instructions on
// MCP initialize. Tests cover the four entry surfaces:
// Content, ToolOutput, ToolName, ToolInput.
func TestSessionContainsHarpName(t *testing.T) {
	harp := "swift-amber-falcon"

	t.Run("matches_Content", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Content: "Your session is named `" + harp + "`. Refer to it…"},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("matches_ToolOutput", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "hi"},
			{Type: backends.EntryTypeAssistant, Content: "hello"},
			{Type: backends.EntryTypeToolResult, ToolName: "task_list",
				ToolOutput: `[{"harp_id":"swift-amber-falcon","text":"x"}]`},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("matches_ToolInput", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeToolUse, ToolName: "TodoWrite",
				ToolInput: json.RawMessage(`{"todos":[{"content":"` + "`" + harp + "`" + ` thing"}]}`)},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("matches_ToolName", func(t *testing.T) {
		// Unusual but possible: a harp-named MCP tool. The matcher
		// shouldn't miss it.
		s := &backends.Session{Entries: []backends.SessionEntry{
			{ToolName: harp},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("no_match_when_absent", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Content: "no harp here"},
			{ToolOutput: "nor here"},
		}}
		assert.False(t, sessionContainsHarpName(s, harp))
	})

	t.Run("empty_session", func(t *testing.T) {
		assert.False(t, sessionContainsHarpName(&backends.Session{}, harp))
	})

	t.Run("nil_session", func(t *testing.T) {
		assert.False(t, sessionContainsHarpName(nil, harp))
	})

	t.Run("empty_harp_never_matches", func(t *testing.T) {
		// An empty harp would substring-match everything. Reject up front.
		s := &backends.Session{Entries: []backends.SessionEntry{{Content: "anything"}}}
		assert.False(t, sessionContainsHarpName(s, ""))
	})
}
