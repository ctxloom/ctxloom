package engine

import (
	"fmt"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// ClaudeCode.Decode really does mix two name-matching disciplines in one
// expression list: ccShellForTool substring-matches, claudeGatesTool exact-
// matches. That alone establishes no reachable consequence, and these two
// tests are the vice that keeps it that way.
//
// Half one: over the GATED set — the only names whose Shell can ever reach a
// decision — the substring discipline yields exactly what an exact-match
// discipline would. The table is exhaustive by construction, so adding a gated
// tool whose name happens to contain "powershell"/"pwsh" without being a
// PowerShell tool (or a PowerShell tool whose name does not) fails here.
func TestClaudeShellDisciplinesAgreeOverGatedSet(t *testing.T) {
	// The exact-match answer, one entry per gated tool.
	exact := map[string]ir.Shell{
		"Bash":         "",
		"PowerShell":   ir.ShellPwsh,
		"Edit":         "",
		"Write":        "",
		"MultiEdit":    "",
		"NotebookEdit": "",
	}

	if len(exact) != len(claudeGatedTools) {
		t.Fatalf("this table has %d entries for %d gated tools — a gated tool was added without deciding its shell hint",
			len(exact), len(claudeGatedTools))
	}
	for _, tool := range claudeGatedTools {
		want, ok := exact[tool]
		if !ok {
			t.Errorf("gated tool %q has no entry in the exact-match table", tool)
			continue
		}
		if got := ccShellForTool(tool); got != want {
			t.Errorf("ccShellForTool(%q) = %q, but the exact-match discipline says %q — "+
				"the two disciplines Decode uses now disagree on a GATED name, which does reach a decision", tool, got, want)
		}
	}
}

// Half two: on every name where the two disciplines CAN disagree — a name the
// substring match claims but the gated list does not — Decode marks the request
// ToolUngated. cmd/ltk/evaluate.go denies on ToolUngated before app.Decide ever
// reads Request.Shell, so the shell hint on such a name is inert. That is why
// the mismatch is a cohesion wart and not a decision defect; if the ungated flag
// ever stopped being set here, it would become one.
func TestClaudeSubstringOnlyMatchesAreUngated(t *testing.T) {
	for _, tool := range []string{
		"MyPowerShellHelper",
		"mcp__shell__pwsh",
		"PowerShellScript",
	} {
		payload := fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":%q,"tool_input":{"command":"whoami"}}`, tool)
		req, err := ClaudeCode{}.Decode([]byte(payload))
		if err != nil {
			t.Fatalf("Decode(%q): %v", tool, err)
		}
		if req.Shell == "" {
			// The disciplines were unified: this name is no longer claimed by
			// the shell hint at all, so there is nothing here to disagree about.
			continue
		}
		if !req.ToolUngated {
			t.Errorf("%q is claimed by the substring discipline but not by the gated list, "+
				"and is no longer marked ungated — its shell hint can now reach a decision", tool)
		}
	}
}
