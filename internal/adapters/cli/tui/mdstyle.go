package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Plan styles: light, and only where the structure is unambiguous. A plan is
// shown for the human to judge, not rendered as a document; a markdown engine
// would be a large dependency whose output the modal would then have to trust.
var (
	styleMDH     = lipgloss.NewStyle().Bold(true)
	styleMDFence = lipgloss.NewStyle().Faint(true)
)

// styleMarkdownLines lays a plan's markdown out as display lines of at most
// width columns: headings bold without their marks, fenced code under a
// gutter and never parsed, bullets as "•", quotes under a gutter. It wraps
// and never truncates. The source is child text, so it is sanitized first.
func styleMarkdownLines(src string, width int) []string {
	width = max(width, 8)
	var out []string
	inFence := false
	for _, line := range strings.Split(sanitizeForDisplay(src), "\n") {
		if isFence(line) {
			inFence = !inFence
			out = append(out, wrapStyled(line, width, styleMDFence)...)
			continue
		}
		if inFence {
			out = append(out, wrapUnder("│ ", line, width)...)
			continue
		}
		out = append(out, styleProseLine(line, width)...)
	}
	return out
}

func isFence(line string) bool {
	t := strings.TrimLeft(line, " ")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// styleProseLine styles one line outside a fence.
func styleProseLine(line string, width int) []string {
	trimmed := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(trimmed)]
	if text, ok := heading(trimmed); ok {
		return wrapStyled(text, width, styleMDH)
	}
	for _, mark := range []string{"- ", "* ", "+ "} {
		if rest, ok := strings.CutPrefix(trimmed, mark); ok {
			return wrapUnder(indent+"• ", rest, width)
		}
	}
	if rest, ok := strings.CutPrefix(trimmed, ">"); ok {
		return wrapUnder(indent+"│ ", strings.TrimPrefix(rest, " "), width)
	}
	return wrapUnder("", line, width)
}

// heading reports an ATX heading (one to six '#' then a space) and its text.
func heading(s string) (text string, ok bool) {
	level := 0
	for level < len(s) && level < 7 && s[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(s) || s[level] != ' ' {
		return "", false
	}
	return strings.TrimSpace(s[level:]), true
}

// wrapStyled word-wraps text to width, then styles each line on its own so
// no style spans a line break.
func wrapStyled(text string, width int, style lipgloss.Style) []string {
	lines := wrapUnder("", text, width)
	for i, l := range lines {
		lines[i] = style.Render(l)
	}
	return lines
}

// wrapUnder word-wraps text to width after lead, continuing under it: a
// continuation line is indented by lead's width so a bullet's text stays in
// its column. A word longer than the room left is broken, never cut.
func wrapUnder(lead, text string, width int) []string {
	room := max(width-lipgloss.Width(lead), 1)
	pad := strings.Repeat(" ", lipgloss.Width(lead))
	parts := strings.Split(ansi.Wrap(text, room, ""), "\n")
	for i, p := range parts {
		if i == 0 {
			parts[i] = lead + p
		} else {
			parts[i] = pad + p
		}
	}
	return parts
}
