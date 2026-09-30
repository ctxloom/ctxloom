package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plain strips the styler's own SGR so assertions read the text.
func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestStyleMarkdownLines_HeadingsAreBoldWithoutTheirMarks(t *testing.T) {
	lines := styleMarkdownLines("# Plan\n## Steps\nbody", 40)
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"Plan", "Steps", "body"}, plain(lines))
	assert.Contains(t, lines[0], "\x1b[1", "a heading is bold")
	assert.Contains(t, lines[1], "\x1b[1", "every heading level is bold")
	assert.NotContains(t, lines[2], "\x1b[", "body text is not styled")
}

func TestStyleMarkdownLines_FencedCodeIsGutteredAndNeverParsed(t *testing.T) {
	lines := plain(styleMarkdownLines("text\n```go\n# not a heading\n- not a bullet\n```\nafter", 40))
	assert.Equal(t, []string{"text", "```go", "│ # not a heading", "│ - not a bullet", "```", "after"}, lines)
}

func TestStyleMarkdownLines_BulletsAndQuotes(t *testing.T) {
	lines := plain(styleMarkdownLines("- one\n* two\n  + nested\n> quoted", 40))
	assert.Equal(t, []string{"• one", "• two", "  • nested", "│ quoted"}, lines)
}

// TestStyleMarkdownLines_WrapsNeverTruncates pins the rule for everything a
// child shows the human: a long line wraps within the width, and every word
// of it is still there.
func TestStyleMarkdownLines_WrapsNeverTruncates(t *testing.T) {
	long := "Refactor the queue so that every parked request carries its lineage and a deadline the modal can count down"
	lines := styleMarkdownLines("## "+long+"\n"+long, 30)
	require.Greater(t, len(lines), 4)
	for _, l := range lines {
		assert.LessOrEqual(t, lipgloss.Width(l), 30, "%q overflows", l)
	}
	joined := strings.Join(strings.Fields(strings.Join(plain(lines), " ")), " ")
	assert.Equal(t, long+" "+long, joined, "nothing was cut")
}

// TestStyleMarkdownLines_SanitizesThePlan: a plan is child text, so hostile
// escapes come out as markers, in headings and fences alike.
func TestStyleMarkdownLines_SanitizesThePlan(t *testing.T) {
	lines := styleMarkdownLines("# Plan\x1b[2J\n```\n\x1b]0;pwned\x07\n```\n\u202eevil", 60)
	all := strings.Join(lines, "\n")
	assert.NotContains(t, all, "\x1b[2J")
	assert.NotContains(t, all, "\x1b]")
	assert.NotContains(t, all, "\a")
	assert.NotContains(t, all, "\u202e")
	assert.Contains(t, all, "⟨ESC⟩[2J")
	assert.Contains(t, all, "⟨ESC⟩]0;pwned⟨U+0007⟩")
	assert.Contains(t, all, "⟨U+202E⟩evil")
}
