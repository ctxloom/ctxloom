package coord

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// summaryMaxRunes bounds a Summary: it is one line in a list, not the
// request — the full request is only ever shown in the originator's modal.
const summaryMaxRunes = 80

// summaryInputFields are the tool-input fields a Summary names, the first
// present one winning: what the call acts on, for the tools that ask most.
var summaryInputFields = [...]string{"command", "file_path", "url", "pattern"}

// Summary is the request in one bounded line, rendered where it parks so
// every viewer shows the same words: a tool request is the tool and what it
// acts on, a question its first header, a plan its path. Everything in it
// is the asking child's, so a character that could act on a terminal or
// hide part of the line (a control, a bidi override, a zero-width, a line
// separator) is written as its escape, never as itself.
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
	return truncateRunes(visible(s), summaryMaxRunes)
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

// visible keeps every graphic rune and writes every other one as its Go
// escape (\x1b, ‮, \n). unicode.IsGraphic excludes exactly the
// controls, format characters and line/paragraph separators.
func visible(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsGraphic(r) {
			b.WriteRune(r)
			continue
		}
		q := strconv.QuoteRuneToGraphic(r)
		b.WriteString(q[1 : len(q)-1])
	}
	return b.String()
}

// truncateRunes cuts s to at most n runes, the last one an ellipsis when
// anything was cut.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
