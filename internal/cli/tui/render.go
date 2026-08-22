package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Role tags per the plan's §4a mockup: fixed-width prefixes so the feed
// column scans vertically.
func roleTag(it feedItem) string {
	switch it.role {
	case "user":
		return "user  >"
	case "assistant":
		return "asst  <"
	case "thinking":
		return "think ~"
	case "tool_use":
		return "tool  ⚙"
	case "tool_result":
		if it.isError {
			return "tool  ✗"
		}
		return "tool  ✓"
	case "notice":
		return "      ⚠"
	default:
		return "sys   ·"
	}
}

// stateGlyph maps roster states onto the plan's glyphs: ● running,
// ◐ waiting (queued/parked/idle), ✓ done.
func stateGlyph(state string) string {
	switch state {
	case "executing", "live":
		return "●"
	case "ended":
		return "✓"
	default:
		return "◐"
	}
}

// renderItem renders one feed item into display lines at width. Collapsed,
// tool calls are one-liners and thinking shows only its first line; expanded
// (x) shows tool input/output and full thinking. Sidechain entries — an
// engine's own in-harness subagent — nest under a "│" gutter.
func renderItem(it feedItem, width int, expanded bool) []string {
	indent := ""
	if it.sidechain {
		indent = "  │ "
	}
	tag := roleTag(it)
	tagW := lipgloss.Width(tag)
	body := width - lipgloss.Width(indent) - tagW - 1
	if body < 8 {
		body = 8
	}
	prefix := indent + tag + " "
	cont := indent + strings.Repeat(" ", tagW+1)

	raw := itemBodyLines(it, body, expanded)
	if len(raw) == 0 {
		raw = []string{""}
	}

	var out []string
	for i, l := range raw {
		p := cont
		if i == 0 {
			p = prefix
		}
		for j, w := range wrapLine(l, body) {
			if i == 0 && j == 0 {
				out = append(out, p+w)
			} else {
				out = append(out, cont+w)
			}
		}
	}
	return out
}

// itemBodyLines is the per-role body of a feed item, before the role tag and
// the wrap are applied. body is the column budget one line has.
func itemBodyLines(it feedItem, body int, expanded bool) []string {
	switch it.role {
	case "tool_use":
		if expanded {
			return append([]string{it.toolName}, splitLines(it.toolInput)...)
		}
		line := it.toolName
		if in := compactOneLine(it.toolInput); in != "" {
			line += " " + in
		}
		return []string{truncateLine(line, body)}
	case "tool_result":
		out := it.toolOutput
		if out == "" {
			out = it.text
		}
		lines := splitLines(out)
		if expanded {
			return append([]string{resultSummary(it, lines, true)}, lines...)
		}
		return []string{resultSummary(it, lines, false)}
	case "thinking":
		lines := splitLines(it.text)
		if expanded || len(lines) <= 1 {
			return lines
		}
		return []string{truncateLine(lines[0], body-2) + " …"}
	default:
		return splitLines(it.text)
	}
}

// resultSummary is the tool_result one-liner: ok/error plus a size cue. The
// "x expands" key hint belongs to the collapsed form only — the expanded form
// already shows the body, and the same rendering is what the txt export and
// the clipboard copy carry, where there is no key to press.
func resultSummary(it feedItem, lines []string, expanded bool) string {
	status := "ok"
	if it.isError {
		status = "error"
	}
	switch {
	case len(lines) == 0 || (len(lines) == 1 && lines[0] == ""):
		return status
	case len(lines) == 1:
		return status + ": " + lines[0]
	case expanded:
		return fmt.Sprintf("%s (%d lines)", status, len(lines))
	default:
		return fmt.Sprintf("%s (%d lines) — x expands", status, len(lines))
	}
}

// compactOneLine squeezes raw JSON input onto one line for the collapsed
// tool call.
func compactOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// wrapLine hard-wraps to width COLUMNS (feed content is left as-is
// otherwise). Feed content is whatever the engine emitted, so the budget is
// counted in columns, not runes: a line of double-width text measured in runes
// runs to twice the pane's width and spills across the divider. It also
// carries SGR colour verbatim from the transcript, so a break may not land
// inside an escape sequence — a half-written CSI makes the terminal swallow
// the rest of the row, the divider and the neighbouring pane with it.
//
// ansi.Hardwrap is byte-preserving with preserveSpace set: joining the result
// reproduces the input exactly, and any style left open at a break is carried
// onto the next line rather than dropped.
func wrapLine(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	out := strings.Split(ansi.Hardwrap(s, width, true), "\n")
	// A single grapheme wider than the whole budget cannot be split, so
	// Hardwrap breaks in front of it and leaves an empty first line. Drop it:
	// the caller's contract is that a non-empty input yields no empty lead.
	// Only a literally empty string qualifies — a first line holding just an
	// SGR introducer is zero-width but must still reach the terminal.
	if len(out) > 1 && out[0] == "" {
		out = out[1:]
	}
	return out
}

func truncateLine(s string, width int) string {
	if width < 1 || lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// renderItems renders the whole feed plus a per-item first-line index (the
// cursor jump table). cursor marks the selected item with a "▸" gutter.
func renderItems(items []feedItem, width int, expanded map[int]bool, cursor int) (lines []string, firstLine []int) {
	for i, it := range items {
		firstLine = append(firstLine, len(lines))
		for j, l := range renderItem(it, width-2, expanded[i]) {
			gutter := "  "
			if i == cursor && j == 0 {
				gutter = "▸ "
			}
			lines = append(lines, gutter+l)
		}
	}
	return lines, firstLine
}
