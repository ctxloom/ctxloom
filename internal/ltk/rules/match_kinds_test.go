package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Command rules and path rules are separate types under separate keys (rules
// and path_rules). The YAML can still name a path under a command rule's match
// or a command under a path rule's, so the split is enforced at load: these
// pin that a mixed shape fails rather than decoding into either kind.

func TestMatchKindsAreMutuallyExclusive(t *testing.T) {
	for _, y := range []string{
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], command: [go] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION] }\n    message: m\n",
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [VERSION], command: [go] }\n    message: m\n",
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [VERSION], args_any: [--force] }\n    message: m\n",
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [VERSION], unless: [--list] }\n    message: m\n",
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [VERSION], shells: [bash] }\n    message: m\n",
		"version: 1\npath_rules:\n  - id: x\n    match: { path: [VERSION], backgrounded: true }\n    message: m\n",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("a match mixing path with command-style conditions must be rejected: %s", y)
		}
	}
}

func TestEachEvaluatorIgnoresTheOtherKind(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
path_rules:
  - id: path-rule
    match: { path: [VERSION] }
    action: deny
    message: no hand edits
rules:
  - id: command-rule
    match: { command: [git, push], args_all: [--force] }
    action: deny
    message: no force push
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Hostility check: each rule must actually bite on its own surface, or the
	// cross-surface assertions below prove nothing.
	if d := Evaluate(cfg, cmd(ir.ShellBash, "git", "push", "--force")); d.Allowed || d.RuleID != "command-rule" {
		t.Fatalf("fixture is not hostile: command rule did not fire (%+v)", d)
	}
	if d := EvaluatePath(cfg, "/proj/VERSION"); d.Allowed || d.RuleID != "path-rule" {
		t.Fatalf("fixture is not hostile: path rule did not fire (%+v)", d)
	}

	// Evaluate must not reach the path rule, and EvaluatePath must not reach
	// the command rule.
	if d := Evaluate(cfg, cmd(ir.ShellBash, "VERSION")); !d.Allowed {
		t.Errorf("Evaluate reached a path rule: %+v", d)
	}
	if d := EvaluatePath(cfg, "/proj/git"); !d.Allowed {
		t.Errorf("EvaluatePath reached a command rule: %+v", d)
	}
}
