package engine

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

func TestClaudeCodeDecodeResolvesShell(t *testing.T) {
	cases := []struct {
		tool string
		want ir.Shell
	}{
		{"Bash", ""}, // defers to $SHELL / config (the Bash tool runs in the login shell)
		{"PowerShell", ir.ShellPwsh},
		{"powershell", ir.ShellPwsh},
		{"Edit", ""},    // not shell-bound
		{"Unknown", ""}, // leave to caller
	}
	for _, c := range cases {
		payload := `{"tool_name":"` + c.tool + `","tool_input":{"command":"x"}}`
		req, err := ClaudeCode{}.Decode([]byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", c.tool, err)
		}
		if req.Shell != c.want {
			t.Errorf("tool %q -> shell %q, want %q", c.tool, req.Shell, c.want)
		}
	}
}
