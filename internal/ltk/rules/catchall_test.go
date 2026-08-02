package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// The two match shapes below carry NO positive selector — nothing that names a
// program, an argument or a path — yet both load and both deny unconditionally
// within their scope, which is the accident Match's own doc says an
// empty match avoids. These tests CHARACTERIZE what ltk does today so the open
// question is visible in the suite rather than only in a findings row:
// tightening validation here would reject rule files that load now, and one of
// the two shapes (`shells:`-only) is arguably a legitimate "deny this whole
// dialect" policy. Whether either should keep loading is a config-contract
// decision, not a sweep's.
//
// The `unless:`-only shape additionally has a deliberately-named pin asserting
// it is valid — TestUnlessParsesAndCountsAsConstraint — which is why the
// obvious remedy (dropping Unless from hasConstraint) was measured and
// reverted rather than landed.

// TestShellsOnlyRuleIsAnUnconditionalDeny records that a rule whose only match
// condition is `shells:` denies EVERY command parsed in that dialect, and
// allows everything in any other dialect.
func TestShellsOnlyRuleIsAnUnconditionalDeny(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: x\n    match: { shells: [bash] }\n    message: m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "ls")); d.Allowed {
		t.Error("a shells:[bash]-only deny rule denies every bash command, including `ls`; that is today's behaviour")
	}
	if d := Evaluate(cfg, cmd(ir.ShellZsh, "ls")); !d.Allowed {
		t.Error("a shells:[bash]-only rule must not reach a zsh command")
	}
}

// TestUnlessOnlyRuleIsAnUnconditionalDeny records that a rule whose only match
// condition is `unless:` denies every command that does not happen to carry one
// of the exception tokens — an exception list with nothing to except from.
func TestUnlessOnlyRuleIsAnUnconditionalDeny(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: x\n    match: { unless: [--help] }\n    message: m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "ls")); d.Allowed {
		t.Error("an unless:-only deny rule denies every command lacking the exception token; that is today's behaviour")
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "ls", "--help")); !d.Allowed {
		t.Error("the exception token must still exempt the command")
	}
}
