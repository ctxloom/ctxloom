package coord

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func toolRequest(tool, input string) PendingApproval {
	return PendingApproval{Kind: ApprovalTool, Ask: engine.PermissionAsk{Kind: engine.AskTool, Tool: tool, Input: json.RawMessage(input)}}
}

// TestPendingApprovalSummary_Kinds: a tool request names the tool and the
// first present of its command, file_path, url or pattern; a question names
// its first header; a plan names its path.
func TestPendingApprovalSummary_Kinds(t *testing.T) {
	cases := []struct {
		name string
		p    PendingApproval
		want string
	}{
		{"command", toolRequest("Bash", `{"command":"make test","description":"x"}`), "Bash: make test"},
		{"file_path", toolRequest("Edit", `{"file_path":"/a/b.go","old_string":"x"}`), "Edit: /a/b.go"},
		{"url", toolRequest("WebFetch", `{"url":"https://example.com","prompt":"p"}`), "WebFetch: https://example.com"},
		{"pattern", toolRequest("Grep", `{"pattern":"TODO","path":"."}`), "Grep: TODO"},
		{"command wins over file_path", toolRequest("X", `{"file_path":"/f","command":"c"}`), "X: c"},
		{"empty field skipped", toolRequest("X", `{"command":"","file_path":"/f"}`), "X: /f"},
		{"no known field", toolRequest("Task", `{"prompt":"go"}`), "Task"},
		{"input not an object", toolRequest("Task", `[1]`), "Task"},
		{"no input", toolRequest("Task", ``), "Task"},
		{"question", PendingApproval{Kind: ApprovalQuestion, Ask: engine.PermissionAsk{Kind: engine.AskQuestion,
			Questions: []engine.Question{{Header: "Pick", Text: "which?"}, {Header: "Second"}}}}, "Pick"},
		{"question without questions", PendingApproval{Kind: ApprovalQuestion}, "question"},
		{"plan", PendingApproval{Kind: ApprovalPlan, Ask: engine.PermissionAsk{Kind: engine.AskPlan,
			Plan: &engine.PlanProposal{Markdown: "# p", Path: "/plans/p.md"}}}, "/plans/p.md"},
		{"plan without path", PendingApproval{Kind: ApprovalPlan, Ask: engine.PermissionAsk{Kind: engine.AskPlan,
			Plan: &engine.PlanProposal{Markdown: "# p"}}}, "plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.p.Summary())
		})
	}
}

// TestPendingApprovalSummary_RawButOneLine: the summary is the child's own
// text, unmarked — each viewer makes it safe for wherever it shows it — but
// it is one line: every run of line breaks becomes one space.
func TestPendingApprovalSummary_RawButOneLine(t *testing.T) {
	assert.Equal(t, "Bash: ls \u202egnp.exe \x1b[2J", toolRequest("Bash", `{"command":"ls \u202egnp.exe \u001b[2J"}`).Summary(),
		"bidi and controls that do not break a line stay raw")
	for _, brk := range []string{"\n", "\r", "\r\n", "\v", "\f", "\u0085", "\u2028", "\u2029", "\n\n\r\n"} {
		in, _ := json.Marshal(map[string]string{"command": "ls" + brk + "rm"})
		assert.Equal(t, "Bash: ls rm", toolRequest("Bash", string(in)).Summary(), "line break %q", brk)
	}
	tool := PendingApproval{Kind: ApprovalTool, Ask: engine.PermissionAsk{Tool: "Ba\nsh"}}
	assert.Equal(t, "Ba sh", tool.Summary(), "the tool name is the child's too")
}

// TestPendingApprovalSummary_Bounded: a summary is one bounded line,
// however long the input, and says it was cut.
func TestPendingApprovalSummary_Bounded(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": strings.Repeat("é", 500)})
	got := toolRequest("Bash", string(in)).Summary()
	assert.Equal(t, summaryMaxRunes, utf8.RuneCountInString(got))
	assert.True(t, strings.HasSuffix(got, "…"), got)
	assert.Equal(t, "Bash: ok", toolRequest("Bash", `{"command":"ok"}`).Summary(), "a short summary is not cut")
}
