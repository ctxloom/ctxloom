package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// U072-F04: the IR records a command's redirects faithfully, but Match has no
// redirect predicate at all, so nothing in the rule language can refer to
// them. For a BARE redirect (`> /etc/passwd`, no program word) that leaves the
// statement structurally unmatchable — matchCommand also bails on empty argv —
// and for an ordinary redirect it means a `path:` rule written to protect a
// file does not cover the shell route to writing it.
//
// These tests characterize that gap rather than close it: giving `match` a
// redirect predicate, or letting path rules see redirect targets, changes what
// a rule file can express and what an existing rule denies. That is a schema
// and policy decision, so the row is ESCALATED and these pins make the
// decision checkable — closing the gap turns them red and forces the prose
// here to be revisited with them.

// bareRedirect is `> target`: valid POSIX with no command word.
func bareRedirect(shell ir.Shell, op, target string) *ir.Script {
	return &ir.Script{Shell: shell, Pipelines: []ir.Pipeline{
		{Commands: []ir.SimpleCommand{{Redirects: []ir.Redirect{{Op: op, Target: target}}}}},
	}}
}

// redirectingCommand is `argv… > target`.
func redirectingCommand(shell ir.Shell, target string, argv ...string) *ir.Script {
	return &ir.Script{Shell: shell, Pipelines: []ir.Pipeline{
		{Commands: []ir.SimpleCommand{{Argv: argv, Redirects: []ir.Redirect{{Op: ">", Target: target}}}}},
	}}
}

func TestBareRedirectIsStructurallyUnmatchable(t *testing.T) {
	script := bareRedirect(ir.ShellBash, ">", "/etc/passwd")

	// Hostility check: the redirect really is in the IR and the command really
	// has no program word, or the assertions below prove nothing.
	cmds := script.Commands()
	if len(cmds) != 1 || len(cmds[0].Redirects) != 1 || cmds[0].Program() != "" {
		t.Fatalf("fixture is not hostile: %+v", cmds)
	}

	// No command rule can reach it: there is no argv to match against.
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: x\n    match: { command: [\"/etc/passwd\"] }\n    message: m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := Evaluate(cfg, script); !d.Allowed {
		t.Error("today no command rule matches a bare redirect; if one now does, this row's decision has been taken")
	}

	// Nor can a path rule: Evaluate skips path rules, which are matched against
	// file-editing tool calls only.
	pathCfg, err := Parse([]byte("version: 1\nrules:\n  - id: y\n    match: { path: [\"/etc/passwd\"] }\n    message: m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := Evaluate(pathCfg, script); !d.Allowed {
		t.Error("today path rules do not see shell redirects; if they now do, this row's decision has been taken")
	}
}

func TestPathRuleDoesNotCoverTheShellRedirectRoute(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: y\n    match: { path: [VERSION] }\n    message: hand-editing VERSION is not allowed\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The tool route is covered.
	if d := EvaluatePath(cfg, "/proj/VERSION"); d.Allowed {
		t.Fatal("fixture is not hostile: the path rule does not even catch the file-edit route")
	}
	// The shell route is not.
	if d := Evaluate(cfg, redirectingCommand(ir.ShellBash, "VERSION", "echo", "9")); !d.Allowed {
		t.Error("today `echo 9 > VERSION` is not caught by a path rule for VERSION; if it now is, this row's decision has been taken")
	}
}
