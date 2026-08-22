package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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

// The "x expands" cue is an instruction to press a key that is only useful
// while the entry is collapsed. Once it IS expanded, repeating the cue tells
// the user to press the key they just pressed — and the same rendering is what
// the txt export and the clipboard copy contain, where no key exists at all.
// The expanded summary keeps the size cue and drops the key hint.
func TestRenderItem_ExpandedToolResultDoesNotAdvertiseTheExpandKey(t *testing.T) {
	it := feedItem{role: "tool_result", toolName: "Bash", toolOutput: "line1\nline2\nline3"}

	collapsed := strings.Join(renderItem(it, 80, false), "\n")
	assert.Contains(t, collapsed, "x expands", "collapsed still offers the key")

	expanded := strings.Join(renderItem(it, 80, true), "\n")
	assert.NotContains(t, expanded, "x expands")
	assert.Contains(t, expanded, "ok (3 lines)", "the size cue survives")
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

// Wrapping is what keeps a feed line inside the feed pane; measured in runes
// it lets a line of double-width content run to twice the pane's width and
// spill across the divider.
func TestWrapLine_WrapsOnDisplayCells(t *testing.T) {
	src := strings.Repeat("日", 10) // 10 runes, 20 columns
	lines := wrapLine(src, 6)
	require.NotEmpty(t, lines)
	for i, l := range lines {
		assert.LessOrEqual(t, lipgloss.Width(l), 6, "line %d: %q", i, l)
	}
	assert.Equal(t, src, strings.Join(lines, ""), "no content is lost to wrapping")
}

// A rune wider than the whole budget must still make progress rather than
// wrap forever.
func TestWrapLine_ARuneWiderThanTheBudgetStillAdvances(t *testing.T) {
	lines := wrapLine("日本", 1)
	assert.Equal(t, []string{"日", "本"}, lines)
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

// Feed content is engine transcript text verbatim, and engines colour it. A
// wrap that lands inside an SGR sequence puts a half-written CSI on the wire;
// the terminal then consumes every following byte until a final byte arrives,
// eating the rest of the row, the pane divider and the neighbouring pane. The
// old per-rune column scan did exactly that, charging the parameter bytes of
// "\x1b[31m" as four columns and cutting between "\x1b[" and "31m".
func TestWrapLine_NeverSplitsAnEscapeSequence(t *testing.T) {
	for _, src := range []string{
		"\x1b[31mERROR\x1b[0m: build failed in pkg/foo",
		"\x1b[38;5;196mred text here\x1b[0m",
		"\x1b[38;2;220;50;47mtruecolour output line\x1b[0m",
		"\x1b[1;4mbold underline\x1b[0m plain \x1b[32mgreen\x1b[0m",
	} {
		for width := 1; width <= 24; width++ {
			lines := wrapLine(src, width)
			require.NotEmpty(t, lines, "src=%q width=%d", src, width)
			for i, l := range lines {
				assertTerminatedEscapes(t, l, "src=%q width=%d line %d", src, width, i)
			}
			assert.Equal(t, src, strings.Join(lines, ""),
				"wrapping loses no bytes: src=%q width=%d", src, width)
		}
	}
}

// The column budget has to be respected on coloured input too: the escape
// bytes cost nothing, so a coloured line must still fill the pane. The old
// scan charged them as columns and emitted 5 visible columns against a
// budget of 10 — at 80 columns that silently discarded most of the pane.
func TestWrapLine_ColouredLinesUseTheWholeColumnBudget(t *testing.T) {
	src := "\x1b[31m" + strings.Repeat("x", 40) + "\x1b[0m"
	lines := wrapLine(src, 10)
	require.Greater(t, len(lines), 1)
	for i, l := range lines[:len(lines)-1] {
		assert.Equal(t, 10, lipgloss.Width(l),
			"every full line spends the whole budget; line %d = %q", i, l)
	}
}

// truncateLine cuts the collapsed tool-call one-liner, which carries the same
// coloured transcript text.
func TestTruncateLine_CutsOnColumnsAndTerminatesEscapes(t *testing.T) {
	src := "\x1b[38;5;196mred text here\x1b[0m"
	for width := 1; width <= 12; width++ {
		got := truncateLine(src, width)
		assertTerminatedEscapes(t, got, "width=%d", width)
		assert.Equal(t, width, lipgloss.Width(got),
			"truncation spends the whole budget: width=%d got=%q", width, got)
	}
	assert.Equal(t, src, truncateLine(src, 40), "a line inside the budget is untouched")
}

// A grapheme cluster is one glyph, not one column per code point. Summing
// per-rune widths charges a ZWJ family six columns and a flag four.
func TestWrapLine_KeepsGraphemeClustersWhole(t *testing.T) {
	for _, cluster := range []string{"\U0001F468‍\U0001F469‍\U0001F467", "\U0001F1EC\U0001F1E7"} {
		src := strings.Repeat(cluster, 4)
		lines := wrapLine(src, 4)
		assert.Equal(t, src, strings.Join(lines, ""), "cluster=%q", cluster)
		for i, l := range lines {
			assert.LessOrEqual(t, lipgloss.Width(l), 4,
				"cluster=%q line %d = %q is over budget", cluster, i, l)
			assert.Equal(t, 0, len(l)%len(cluster),
				"cluster=%q line %d = %q split a cluster", cluster, i, l)
		}
	}
}

// assertTerminatedEscapes fails if s contains an ANSI escape sequence that is
// cut short — an ESC with nothing after it, or a CSI whose final byte
// (0x40-0x7e) never arrives. Those are the bytes that make a terminal swallow
// the rest of the screen, so this asserts on what is written, not on how a
// pane looks.
func assertTerminatedEscapes(t *testing.T, s string, msg string, args ...any) {
	t.Helper()
	ctx := fmt.Sprintf(msg, args...)
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		if i+1 >= len(s) {
			t.Errorf("%s: trailing bare ESC in %q", ctx, s)
			return
		}
		if s[i+1] != '[' {
			continue // not a CSI; the two-byte forms are complete as they are
		}
		terminated := false
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				terminated = true
				i = j
				break
			}
			if s[j] < 0x20 || s[j] > 0x3f {
				break // not a valid parameter/intermediate byte
			}
		}
		if !terminated {
			t.Errorf("%s: unterminated CSI at byte %d in %q", ctx, i, s)
			return
		}
	}
}

// The production reach: itemsFromFeedEvent copies engine transcript content
// into feedItem.text verbatim, and engines colour that content, so every byte
// renderItems emits at real terminal geometry has to be safe to write. This
// covers all three cut sites at once — the wrap in renderItem, the
// truncateLine in the collapsed tool_use and thinking bodies.
func TestRenderItems_EmitsNoTornEscapesOverColouredTranscriptContent(t *testing.T) {
	red := "\x1b[38;2;220;50;47m"
	items := []feedItem{
		{role: "assistant", text: red + "an assistant reply that runs well past the pane\x1b[0m"},
		{role: "thinking", text: "\x1b[2mfirst thought line\x1b[0m\nsecond line\nthird line"},
		{role: "tool_use", toolName: "\x1b[36mBash\x1b[0m", toolInput: `{"command":"` + red + `go build ./...\x1b[0m"}`},
		{role: "tool_result", toolOutput: "\x1b[31mERROR\x1b[0m: build failed in pkg/foo\nand a second line"},
		{role: "notice", text: "\x1b[33m… 12 live events dropped\x1b[0m"},
	}
	for _, width := range []int{20, 40, 47, 60, 80, 120} {
		lines, first := renderItems(items, width, map[int]bool{1: true, 3: true}, 0)
		require.Len(t, first, len(items), "width=%d", width)
		require.NotEmpty(t, lines, "width=%d", width)
		for i, l := range lines {
			assertTerminatedEscapes(t, l, "width=%d line %d", width, i)
			assert.LessOrEqual(t, lipgloss.Width(l), width,
				"width=%d line %d overflows the pane: %q", width, i, l)
		}
	}
}
