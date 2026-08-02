package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// twoDenyRules denies `rm` with rule "first" and `git push --force` with rule
// "second", in that file order.
func twoDenyRules(t *testing.T) *Config {
	t.Helper()
	cfg, err := Parse([]byte(`
version: 1
rules:
  - id: first
    match: { command: [rm] }
    action: deny
    message: no rm
  - id: second
    match: { command: [git, push, --force] }
    action: deny
    message: no force push
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// TestCommandOrderDominatesRuleOrder pins the invariant Evaluate's corrected
// doc now states. Evaluate's outer loop is the COMMAND walk and its
// inner loop is the rule list, so the denial that is reported belongs to the
// earliest matching COMMAND, not the earliest matching RULE. Which rule fires
// is what the operator sees as the reason and the suggestion, so the ordering
// has to be described accurately even though either way the line is denied.
func TestCommandOrderDominatesRuleOrder(t *testing.T) {
	cfg := twoDenyRules(t)

	// `git push --force` is command #1 but matches rule #2; `rm` is command #2
	// and matches rule #1. A literal "first matching deny RULE wins" would
	// report "first"; what actually wins is the earlier command's rule.
	script := &ir.Script{Shell: ir.ShellBash, Pipelines: []ir.Pipeline{
		{Commands: []ir.SimpleCommand{{Argv: []string{"git", "push", "--force"}}}},
		{Connector: ir.ConnAnd, Commands: []ir.SimpleCommand{{Argv: []string{"rm", "x"}}}},
	}}
	d := Evaluate(cfg, script)
	if d.Allowed {
		t.Fatal("both commands trip a deny rule; the line must be denied")
	}
	if d.RuleID != "second" {
		t.Errorf("RuleID = %q, want \"second\": the earlier COMMAND's rule wins, not the earlier RULE", d.RuleID)
	}

	// Reversing the commands reverses the reported rule, with the rule file
	// untouched — which is the whole point.
	script.Pipelines[0], script.Pipelines[1] = script.Pipelines[1], script.Pipelines[0]
	if d := Evaluate(cfg, script); d.RuleID != "first" {
		t.Errorf("RuleID = %q, want \"first\" once `rm` is the earlier command", d.RuleID)
	}
}

// TestRuleOrderStillDecidesWithinOneCommand is the other half: for a SINGLE
// command the rule list really is scanned in order and the first match wins,
// which is the part of the original wording that was right.
func TestRuleOrderStillDecidesWithinOneCommand(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
rules:
  - id: allow-status
    match: { command: [git, status] }
    action: allow
  - id: deny-git
    match: { command: [git] }
    action: deny
    message: no git
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "git", "status")); !d.Allowed {
		t.Error("the earlier allow rule must win over the later deny rule for the same command")
	}
	if d := Evaluate(cfg, cmd(ir.ShellBash, "git", "push")); d.Allowed {
		t.Error("a command the allow rule does not cover must still reach the deny rule")
	}
}
