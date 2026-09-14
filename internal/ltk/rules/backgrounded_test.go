package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// cmdBG builds a one-command script like cmd, but with SimpleCommand.Background
// set directly — the shell frontend is what normally sets this from a trailing
// `&` (see frontend/shell's TestBackgroundAndSequence and friends); this
// package tests the predicate against the IR shape the frontend produces,
// without depending on a real parse.
func cmdBG(shell ir.Shell, background bool, argv ...string) *ir.Script {
	return &ir.Script{
		Shell:     shell,
		Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: argv, Background: background}}}},
	}
}

const backgroundedYAML = `
version: 1
rules:
  - id: background-via-harness
    match: { backgrounded: true }
    message: "detached jobs fire no completion notification"
    suggest: "run the same command with run_in_background: true"
`

// TestBackgroundedMatchesTrailingAmpersand is the case the whole predicate
// exists for: `just test-acceptance &` — a command with no distinguishing
// program name a command-head rule could ever list — is caught because the
// frontend recorded the trailing `&` on the command itself.
func TestBackgroundedMatchesTrailingAmpersand(t *testing.T) {
	cfg := mustParse(t, backgroundedYAML)
	d := Evaluate(cfg, cmdBG(ir.ShellBash, true, "just", "test-acceptance"))
	if d.Allowed {
		t.Fatal("a command flagged Background by the frontend should be denied")
	}
	if d.RuleID != "background-via-harness" {
		t.Fatalf("wrong rule: %+v", d)
	}
}

// TestBackgroundedMatchesDetachWrappers covers nohup and setsid — the two
// spellings the OLD command-head rules already caught — plus disown, which no
// command-head rule ever named. All three hand a job off without leaving
// anything for the caller to wait on, the same as a trailing `&`.
func TestBackgroundedMatchesDetachWrappers(t *testing.T) {
	cfg := mustParse(t, backgroundedYAML)
	for _, argv := range [][]string{
		{"nohup", "just", "test"},
		{"setsid", "just", "test"},
		{"disown"},
	} {
		if Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("%v should be denied: it is a detach wrapper", argv)
		}
	}
}

// TestBackgroundedDoesNotMatchPlainCommand is the negative the old rule got
// wrong: a bare `just test` (no trailing `&`, no detach wrapper) must not be
// caught — this is the ordinary, harness-visible foreground case the rule
// must leave alone.
func TestBackgroundedDoesNotMatchPlainCommand(t *testing.T) {
	cfg := mustParse(t, backgroundedYAML)
	if !Evaluate(cfg, cmdBG(ir.ShellBash, false, "just", "test")).Allowed {
		t.Error("plain `just test` (Background=false) should be allowed")
	}
	if !Evaluate(cfg, cmd(ir.ShellBash, "just", "test")).Allowed {
		t.Error("plain `just test` (Background unset, zero value) should be allowed")
	}
}

// TestBackgroundedFalseIsNoConstraint pins the same "absent means no
// constraint" convention every other Match field already follows: an explicit
// `backgrounded: false` behaves exactly like omitting the field, not like a
// positive requirement that the command NOT be backgrounded.
func TestBackgroundedFalseIsNoConstraint(t *testing.T) {
	cfg := mustParse(t, `
version: 1
rules:
  - id: no-op-rule
    match: { command: [go, test], backgrounded: false }
    message: "should behave exactly like backgrounded absent"
`)
	// `backgrounded: false` contributes nothing; the rule still fires purely
	// off `command: [go, test]`, whether or not the command is detached.
	if Evaluate(cfg, cmd(ir.ShellBash, "go", "test")).Allowed {
		t.Error("go test should still be denied by the command match")
	}
	if Evaluate(cfg, cmdBG(ir.ShellBash, true, "go", "test")).Allowed {
		t.Error("go test & should still be denied by the command match, backgrounded:false does not exempt it")
	}
}

// TestBackgroundedAloneIsAValidConstraint proves `match: { backgrounded: true
// }` with nothing else is accepted (hasConstraint) and parses.
func TestBackgroundedAloneIsAValidConstraint(t *testing.T) {
	cfg := mustParse(t, backgroundedYAML)
	if !cfg.Rules[0].Match.hasConstraint() {
		t.Error("`backgrounded: true` alone should be a valid match constraint")
	}
}

// TestBackgroundedCannotCombineWithPath pins that match.path (a file-edit
// rule) and match.backgrounded (a command-only condition) are mutually
// exclusive, the same as match.path is with command/args/shells/unless.
func TestBackgroundedCannotCombineWithPath(t *testing.T) {
	_, err := Parse([]byte(`
version: 1
rules:
  - id: bad
    match: { path: ["VERSION"], backgrounded: true }
    message: m
`))
	if err == nil {
		t.Fatal("match.path combined with match.backgrounded should be rejected")
	}
}

// TestBackgroundedFailsOpenOnUnrelatedCommand is the fail-open property
// restated at the rules layer: a config carrying a `backgrounded: true` rule
// denies nothing when handed a script with no commands at all (the shape
// Evaluate sees whenever a caller could not build any IR for the input, e.g.
// on_parse_error's own fail-open path never even calls Evaluate). See
// app_test.go's TestUnparseableCommandPassesThroughWithBackgroundedRule for
// the end-to-end version through the real on_parse_error policy.
func TestBackgroundedFailsOpenOnUnrelatedCommand(t *testing.T) {
	cfg := mustParse(t, backgroundedYAML)
	if !Evaluate(cfg, &ir.Script{Shell: ir.ShellBash}).Allowed {
		t.Error("an empty script must be allowed even with a backgrounded rule present")
	}
	if !Evaluate(cfg, nil).Allowed {
		t.Error("a nil script must be allowed even with a backgrounded rule present")
	}
}
