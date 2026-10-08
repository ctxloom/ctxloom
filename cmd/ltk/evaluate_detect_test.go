package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hook command that names no engine — the bundle's `ltk evaluate`, shared
// by every engine ctxloom delivers it to — answers in the wire of the engine
// whose payload arrived: opencode's bridge payload (whose tool input also
// spells `command`, as claude's does) is opencode's, claude's is claude's.
func TestEvaluateWithoutEngine_AnswersInThePayloadsWire(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "rules.yaml")
	cfg := "schema_version: 1\nrules:\n  - id: no-force-push\n    match: { command: [git, push], args_all: [--force] }\n    message: no force pushes\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, payload, want string }{
		{"opencode", `{"event":"tool.execute.before","session":"s","tool":"bash","tool_input":{"command":"git push --force"}}`, `"block":true`},
		{"claude", `{"tool_name":"Bash","tool_input":{"command":"git push --force"}}`, `"permissionDecision":"deny"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := evaluate("", cfgPath, "", strings.NewReader(c.payload))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out.Stdout), c.want) || !strings.Contains(string(out.Stdout), "no force pushes") {
				t.Errorf("want %s carrying the rule's message, got %s", c.want, out.Stdout)
			}
		})
	}
	out, err := evaluate("", cfgPath, "", strings.NewReader(`{"event":"tool.execute.before","tool":"bash","tool_input":{"command":"git status"}}`))
	if err != nil || len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Errorf("an allowed opencode command writes nothing, got %+v, %v", out, err)
	}
}
