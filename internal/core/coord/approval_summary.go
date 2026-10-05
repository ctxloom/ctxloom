package coord

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// summaryMaxRunes bounds a Summary: it is one line in a list, not the
// request — the full request is only ever shown in the originator's modal.
const summaryMaxRunes = 80

// summaryInputFields are the tool-input fields a Summary names, the first
// present one winning: what the call acts on, for the tools that ask most.
var summaryInputFields = [...]string{"command", "file_path", "url", "pattern"}

// Summary is the request in one bounded line, composed where it parks so
// every viewer shows the same words: a tool request is the tool and what it
// acts on, a question its first header, a plan its path. It is RAW — the
// asking child's own characters — because it travels to programs as well as
// terminals: each viewer makes it safe for wherever it shows it
// (displaysafe.Text on a terminal), and a JSON consumer gets the text itself.
// Only line breaks are not kept: each run of them is one space, so the line
// stays a line however it is shown.
func (p PendingApproval) Summary() string {
	var s string
	switch p.Kind {
	case ApprovalQuestion:
		s = "question"
		if len(p.Ask.Questions) > 0 && p.Ask.Questions[0].Header != "" {
			s = p.Ask.Questions[0].Header
		}
	case ApprovalPlan:
		s = "plan"
		if p.Ask.Plan != nil && p.Ask.Plan.Path != "" {
			s = p.Ask.Plan.Path
		}
	default:
		s = p.Ask.Tool
		if target := toolTarget(p.Ask.Input); target != "" {
			s += ": " + target
		}
	}
	return truncateRunes(oneLine(s), summaryMaxRunes)
}

// toolTarget is the first non-empty string among summaryInputFields in a
// tool call's input; empty when the input is not an object or names none.
func toolTarget(input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	for _, k := range summaryInputFields {
		if v, ok := fields[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// oneLine replaces each run of line breaks with one space. Invalid UTF-8
// becomes U+FFFD: the summary is a proto string field, which must be valid
// UTF-8 to marshal at all.
func oneLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inBreak := false
	for _, r := range strings.ToValidUTF8(s, string(utf8.RuneError)) {
		if isLineBreak(r) {
			if !inBreak {
				b.WriteByte(' ')
			}
			inBreak = true
			continue
		}
		inBreak = false
		b.WriteRune(r)
	}
	return b.String()
}

// isLineBreak reports whether r ends a line on some terminal or in some
// renderer: LF, VT, FF, CR, NEL, and the Unicode line and paragraph
// separators.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\v', '\f', '\r', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// truncateRunes cuts s to at most n runes, the last one an ellipsis when
// anything was cut.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
