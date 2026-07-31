package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// U073-F07 reads Match as "two types wearing one name", with
// mixesCommandAndPath enforcing at runtime what a type split would enforce at
// compile time. The description is accurate: Match carries the command-rule
// fields and the path-rule field in one struct, isPathRule discriminates, and
// the two evaluators each skip the other kind.
//
// The row is ESCALATED because every way to split it is larger than a sweep:
// splitting the Go types means Rule.Match becomes a union or an interface and
// every call site plus Config's consumers in app and cmd/ltk change with it,
// while splitting the YAML keys is a config-format change outright.
//
// It is also worth recording that the compile-time benefit is only partial.
// Match is decoded from user-written YAML, so a file naming both `command:`
// and `path:` remains representable whatever the Go types are; a split moves
// the rejection into UnmarshalYAML rather than removing it. What a split would
// really buy is that no INTERNAL caller can construct the mixed shape.
//
// These pins are the union discipline itself — the contract any split has to
// preserve, and the thing that would silently regress if one were attempted.

func TestMatchKindsAreMutuallyExclusive(t *testing.T) {
	for _, y := range []string{
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], command: [go] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], args_any: [--force] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], args_all: [--force] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], unless: [--list] }\n    message: m\n",
		"version: 1\nrules:\n  - id: x\n    match: { path: [VERSION], shells: [bash] }\n    message: m\n",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("a match mixing path with command-style conditions must be rejected: %s", y)
		}
	}
}

func TestEachEvaluatorIgnoresTheOtherKind(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
rules:
  - id: path-rule
    match: { path: [VERSION] }
    action: deny
    message: no hand edits
  - id: command-rule
    match: { command: [git, push, --force] }
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
