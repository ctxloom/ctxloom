package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NotebookEdit regression: its payload carries notebook_path (not file_path),
// and a decode that dropped the path made Decide allow every notebook edit —
// the installed matcher advertises NotebookEdit, so path rules must bind it
// exactly like Edit/Write. This mirrors the command-payload deny test in
// TestEvaluateDeniesAndAllows, end to end through evaluate.
func TestEvaluateDeniesNotebookEditByPathRule(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := `version: 1
rules:
  - id: no-notebook-edits
    match: { path: ["*.ipynb"] }
    message: "notebooks are generated"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := `{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"/proj/analysis.ipynb","new_source":"print(1)"}}`
	out, err := evaluate("claude-code", cfgPath, "", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 {
		t.Errorf("claude-code denials exit 0, got %d", out.ExitCode)
	}
	if !strings.Contains(string(out.Stdout), `"deny"`) || !strings.Contains(string(out.Stdout), "notebooks are generated") {
		t.Errorf("a NotebookEdit hitting a path rule must be denied, got %s", out.Stdout)
	}

	// A notebook outside the rule still passes through silently.
	allowed := `{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"/proj/notes.md"}}`
	allowOut, err := evaluate("claude-code", cfgPath, "", strings.NewReader(allowed))
	if err != nil {
		t.Fatal(err)
	}
	if len(allowOut.Stdout) != 0 || allowOut.ExitCode != 0 {
		t.Errorf("allow must be a zero Output (pass-through), got %+v", allowOut)
	}
}
