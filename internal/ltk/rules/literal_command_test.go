package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// These pin the invariant Match.Command's "Tokens are LITERAL" section now
// states: match.command compares for equality while match.path
// globs, and nothing anywhere warns about the difference. The pins exist so
// the documented contract is maintained rather than merely asserted — if
// globbing is ever added to command tokens, both of these go red and the
// prose has to be revisited with them.

func literalCommandRules(t *testing.T, pattern string) *Config {
	t.Helper()
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: x\n    match: { command: " + pattern + " }\n    message: m\n"))
	if err != nil {
		t.Fatalf("Parse(%s): %v", pattern, err)
	}
	return cfg
}

// TestCommandTokenIsNotAGlob is the trap half: a metacharacter in a command
// token buys nothing and the rule silently never fires.
func TestCommandTokenIsNotAGlob(t *testing.T) {
	cfg := literalCommandRules(t, `[git, "push*"]`)
	if d := Evaluate(cfg, cmd(ir.ShellBash, "git", "push", "--force")); !d.Allowed {
		t.Error("`push*` must not glob-match `push`: command tokens are literal")
	}
}

// TestCommandTokenMatchesAMetacharacterLiterally is the half that makes
// rejecting metacharacters at parse time the wrong remedy: argv reaches the
// matcher un-globbed, so a literal `*` argument is matched by a literal `*`
// token, and that rule works today.
func TestCommandTokenMatchesAMetacharacterLiterally(t *testing.T) {
	cfg := literalCommandRules(t, `[rm, "*"]`)
	if d := Evaluate(cfg, cmd(ir.ShellBash, "rm", "*")); d.Allowed {
		t.Error("a literal `*` token must still match a literal `*` argument")
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "rm", "notes.txt")); !d.Allowed {
		t.Error("a literal `*` token must not match an arbitrary argument")
	}
}

// TestPathPatternDoesGlob is the contrasting field, asserted here so the
// asymmetry the prose describes is measured rather than claimed.
func TestPathPatternDoesGlob(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: x\n    match: { path: [\"*.lock\"] }\n    message: m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := EvaluatePath(cfg, "/proj/a/b/go.lock"); d.Allowed {
		t.Error("match.path patterns ARE globs; `*.lock` must catch go.lock")
	}
}
