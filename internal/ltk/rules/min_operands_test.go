package rules

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// gitIdentityYAML is the shape .ltk/config.yaml's identity guard takes with
// min_operands: the WRITE form `git config [--local] user.name VALUE` carries
// three operands (config, the key, the value) and the bare READ form carries
// two. `--unset`/`--unset-all` is a write with only two operands — the arity
// min_operands cannot see — so it needs a rule of its own, keyed on the flag.
const gitIdentityYAML = `
version: 1
rules:
  - id: no-git-config-local-identity
    match:
      command: [git, config]
      args_any: ["user.name", "user.email"]
      min_operands: 3
      unless: ["--global", "--system", "--get", "--get-all", "--get-regexp", "--list", "-l", "--show-origin"]
    message: m
  - id: no-git-config-local-identity-unset
    match:
      command: [git, config]
      args_any: ["user.name", "user.email"]
      args_all: ["--unset"]
      unless: ["--global", "--system"]
    message: m
`

func TestMinOperands_SeparatesIdentityReadsFromWrites(t *testing.T) {
	cfg := mustParse(t, gitIdentityYAML)
	cases := []struct {
		argv    []string
		allowed bool
		why     string
	}{
		{[]string{"git", "config", "user.name"}, true, "a bare read has two operands"},
		{[]string{"git", "config", "user.email"}, true, "a bare read has two operands"},
		{[]string{"git", "config", "user.name", "Bob"}, false, "a write has a third operand, the value"},
		{[]string{"git", "config", "--local", "user.email", "b@x"}, false, "an option is not an operand; the value still is"},
		{[]string{"git", "config", "--add", "user.name", "Bob"}, false, "--add writes"},
		{[]string{"git", "config", "--get", "user.name"}, true, "--get is exempt by unless"},
		{[]string{"git", "config", "--global", "user.name", "Bob"}, true, "--global is exempt by unless"},
		{[]string{"git", "config", "--unset", "user.name"}, false, "--unset writes with two operands: the second rule"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			d := Evaluate(cfg, cmd(ir.ShellBash, tc.argv...))
			if d.Allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v (%s); rule %q", d.Allowed, tc.allowed, tc.why, d.RuleID)
			}
		})
	}
}

// TestMinOperands_CountsAfterTheProgram pins the boundary exactly: N operands
// satisfies min_operands: N, N-1 does not.
func TestMinOperands_CountsAfterTheProgram(t *testing.T) {
	cfg := mustParse(t, "version: 1\nrules:\n  - id: x\n    match: { command: [tool], min_operands: 2 }\n    message: m\n")
	if d := Evaluate(cfg, cmd(ir.ShellBash, "tool", "-v", "a")); !d.Allowed {
		t.Fatal("one operand must not satisfy min_operands: 2")
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "tool", "-v", "a", "b")); d.Allowed {
		t.Fatal("two operands must satisfy min_operands: 2")
	}
}

func TestMinOperands_Validation(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"negative": {
			"version: 1\nrules:\n  - id: x\n    match: { command: [git], min_operands: -1 }\n    message: m\n",
			"match.min_operands must not be negative",
		},
		"without command": {
			"version: 1\nrules:\n  - id: x\n    match: { args_any: [a], min_operands: 2 }\n    message: m\n",
			"match.min_operands needs match.command",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
