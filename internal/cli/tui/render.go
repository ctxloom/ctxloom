package tui

import (
	"fmt"
	"strings"
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
	body := width - len([]rune(indent)) - len([]rune(tag)) - 1
	if body < 8 {
		body = 8
	}
	prefix := indent + tag + " "
	cont := indent + strings.Repeat(" ", len([]rune(tag))+1)

	var raw []string
	switch it.role {
	case "tool_use":
		line := it.toolName
		if in := compactOneLine(it.toolInput); in != "" {
			line += " " + in
		}
		if expanded {
			raw = append(raw, it.toolName)
			raw = append(raw, splitLines(it.toolInput)...)
		} else {
			raw = []string{truncateLine(line, body)}
		}
	case "tool_result":
		out := it.toolOutput
		if out == "" {
			out = it.text
		}
		lines := splitLines(out)
		if expanded {
			raw = append([]string{resultSummary(it, lines)}, lines...)
		} else {
			raw = []string{resultSummary(it, lines)}
		}
	case "thinking":
		lines := splitLines(it.text)
		if expanded || len(lines) <= 1 {
			raw = lines
		} else {
			raw = []string{truncateLine(lines[0], body-2) + " …"}
		}
	default:
		raw = splitLines(it.text)
	}
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

// resultSummary is the tool_result one-liner: ok/error plus a size cue.
func resultSummary(it feedItem, lines []string) string {
	status := "ok"
	if it.isError {
		status = "error"
	}
	switch {
	case len(lines) == 0 || (len(lines) == 1 && lines[0] == ""):
		return status
	case len(lines) == 1:
		return status + ": " + lines[0]
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

// wrapLine hard-wraps by rune count (feed content is left as-is otherwise).
func wrapLine(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	r := []rune(s)
	if len(r) <= width {
		return []string{s}
	}
	var out []string
	for len(r) > width {
		out = append(out, string(r[:width]))
		r = r[width:]
	}
	return append(out, string(r))
}

func truncateLine(s string, width int) string {
	r := []rune(s)
	if width < 1 || len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
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
