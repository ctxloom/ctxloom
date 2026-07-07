package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderItem_RoleTags(t *testing.T) {
	for _, c := range []struct {
		item feedItem
		tag  string
	}{
		{feedItem{role: "user", text: "hi"}, "user  >"},
		{feedItem{role: "assistant", text: "hello"}, "asst  <"},
		{feedItem{role: "thinking", text: "hmm"}, "think ~"},
		{feedItem{role: "tool_use", toolName: "Edit"}, "tool  ⚙"},
		{feedItem{role: "tool_result", toolOutput: "done"}, "tool  ✓"},
		{feedItem{role: "tool_result", toolOutput: "bad", isError: true}, "tool  ✗"},
		{feedItem{role: "system", text: "sys"}, "sys   ·"},
	} {
		lines := renderItem(c.item, 80, false)
		require.NotEmpty(t, lines)
		assert.True(t, strings.HasPrefix(lines[0], c.tag), "%s: got %q", c.item.role, lines[0])
	}
}

func TestRenderItem_ToolUseOneLinerAndExpansion(t *testing.T) {
	it := feedItem{role: "tool_use", toolName: "Edit", toolInput: "{\n  \"file\": \"tokenizer.go\",\n  \"hunks\": 2\n}"}

	collapsed := renderItem(it, 80, false)
	require.Len(t, collapsed, 1, "collapsed tool call is a one-liner")
	assert.Contains(t, collapsed[0], "Edit")
	assert.Contains(t, collapsed[0], `{ "file": "tokenizer.go", "hunks": 2 }`)

	expanded := renderItem(it, 80, true)
	assert.Greater(t, len(expanded), 1, "expanded shows the raw input")
	assert.Contains(t, strings.Join(expanded, "\n"), `"file": "tokenizer.go"`)
}

func TestRenderItem_ToolResultSummaryAndExpansion(t *testing.T) {
	it := feedItem{role: "tool_result", toolName: "Bash", toolOutput: "line1\nline2\nline3"}

	collapsed := renderItem(it, 80, false)
	require.Len(t, collapsed, 1)
	assert.Contains(t, collapsed[0], "ok (3 lines)")

	expanded := renderItem(it, 80, true)
	assert.Contains(t, strings.Join(expanded, "\n"), "line2")
}

func TestRenderItem_ThinkingCollapses(t *testing.T) {
	it := feedItem{role: "thinking", text: "first thought\nsecond thought\nthird"}
	collapsed := renderItem(it, 80, false)
	require.Len(t, collapsed, 1, "multi-line thinking collapses to its first line")
	assert.Contains(t, collapsed[0], "first thought")
	assert.Contains(t, collapsed[0], "…")

	expanded := renderItem(it, 80, true)
	assert.Contains(t, strings.Join(expanded, "\n"), "second thought")
}

func TestRenderItem_SidechainNests(t *testing.T) {
	it := feedItem{role: "assistant", text: "subagent says", sidechain: true}
	lines := renderItem(it, 80, false)
	assert.True(t, strings.HasPrefix(lines[0], "  │ "), "sidechain entries nest visually: %q", lines[0])
}

func TestRenderItem_WrapsLongLines(t *testing.T) {
	it := feedItem{role: "user", text: strings.Repeat("a", 100)}
	lines := renderItem(it, 40, false)
	assert.Greater(t, len(lines), 1, "long content wraps instead of truncating")
	joined := strings.Join(lines, "")
	assert.Equal(t, 100, strings.Count(joined, "a"), "no content lost to wrapping")
}

func TestRenderItems_CursorGutterAndIndex(t *testing.T) {
	items := []feedItem{
		{role: "user", text: "one"},
		{role: "assistant", text: "two"},
	}
	lines, first := renderItems(items, 60, map[int]bool{}, 1)
	require.Len(t, first, 2)
	assert.True(t, strings.HasPrefix(lines[first[1]], "▸ "), "cursor item carries the gutter marker")
	assert.True(t, strings.HasPrefix(lines[first[0]], "  "))
}
