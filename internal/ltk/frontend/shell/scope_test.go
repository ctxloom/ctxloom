package shell_test

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// forcePushRules denies exactly `git push --force`.
func forcePushRules(t *testing.T) *rules.Config {
	t.Helper()
	cfg, err := rules.Parse([]byte(`
version: 1
rules:
  - id: no-force-push
    match:
      command: [git, push, --force]
    action: deny
    message: force pushes are not allowed
`))
	if err != nil {
		t.Fatalf("rules.Parse: %v", err)
	}
	return cfg
}

// requireNestedAssignment is the §11k guard for the subshell-scope pins: it
// asserts the fixture really does carry an assignment inside the subshell, so
// a green result cannot mean "the nested scope was never lowered at all".
func requireNestedAssignment(t *testing.T, s *ir.Script, name, value string) {
	t.Helper()
	for _, c := range s.Commands() {
		for _, a := range c.Assignments {
			if a.Name == name && a.Value == value {
				return
			}
		}
	}
	t.Fatalf("fixture is not hostile: no %s=%s assignment was lowered anywhere in the script", name, value)
}

// TestSubshellAssignmentDoesNotEscape pins a real bypass: a command substitution
// runs in its own process, so an assignment inside it cannot change the
// enclosing script's view of that variable. Sharing one flat `vars` map across
// the whole lowering made it do exactly that — and because the leaked value
// SHADOWS the outer one, it is a rule miss rather than a mere inaccuracy:
// `$x` resolves to the inner `echo` instead of the outer `git`, so the
// force-push deny rule never sees a `git` command at all.
func TestSubshellAssignmentDoesNotEscape(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"command substitution", `x=git; echo $(x=echo); $x push --force`},
		{"backtick substitution", "x=git; echo `x=echo`; $x push --force"},
		{"explicit subshell", `x=git; ( x=echo ); $x push --force`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := shell.New().Parse(context.Background(), ir.ShellBash, tc.src)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.src, err)
			}
			requireNestedAssignment(t, s, "x", "echo")

			var got []string
			for _, c := range s.Commands() {
				if c.Program() == "git" || c.Program() == "echo" {
					got = append(got, c.Program())
				}
			}
			if d := rules.Evaluate(forcePushRules(t), s); d.Allowed {
				t.Errorf("Evaluate(%q) allowed; the subshell assignment leaked and hid the real `git push --force` (programs seen: %v)", tc.src, got)
			}
		})
	}
}

// TestBraceGroupAssignmentDoesEscape is the counterpart: a `{ …; }` group is
// NOT a subshell, so its assignments genuinely do affect later commands and
// must keep doing so. Without this, "give nested scopes their own map" could
// be over-applied and quietly stop resolving a variable a real shell resolves.
func TestBraceGroupAssignmentDoesEscape(t *testing.T) {
	const src = `{ x=git; }; $x push --force`
	s, err := shell.New().Parse(context.Background(), ir.ShellBash, src)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", src, err)
	}
	if d := rules.Evaluate(forcePushRules(t), s); d.Allowed {
		t.Errorf("Evaluate(%q) allowed; a brace group is not a subshell, so x must still resolve to git", src)
	}
}
