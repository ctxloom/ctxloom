package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

const sampleYAML = `
version: 1
rules:
  - id: go-test-to-just
    match: { command: [go, test] }
    action: deny
    message: "Use just test."
    suggest: "just test"
  - id: no-docker-build
    match: { command: docker, args_any: [build, buildx] }
    message: "Builds go through CI."
  - id: no-shell-wrapper
    match: { command: "sh -c" }
    message: "No sh -c."
`

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// cmd builds a one-command script for a shell.
func cmd(shell ir.Shell, argv ...string) *ir.Script {
	return &ir.Script{
		Shell:     shell,
		Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: argv}}}},
	}
}

func TestDenyMatch(t *testing.T) {
	cfg := mustParse(t, sampleYAML)
	d := Evaluate(cfg, cmd(ir.ShellBash, "go", "test", "./..."))
	if d.Allowed {
		t.Fatal("go test should be denied")
	}
	if d.Rule == nil || d.Rule.ID != "go-test-to-just" {
		t.Fatalf("wrong rule: %+v", d.Rule)
	}
	if d.Suggest != "just test" {
		t.Errorf("suggest = %q", d.Suggest)
	}
}

func TestAllowWhenNoRuleMatches(t *testing.T) {
	cfg := mustParse(t, sampleYAML)
	if d := Evaluate(cfg, cmd(ir.ShellBash, "go", "build", "./...")); !d.Allowed {
		t.Errorf("go build should be allowed, got %+v", d)
	}
}

func TestDefaultActionIsDeny(t *testing.T) {
	cfg := mustParse(t, sampleYAML)
	// no-docker-build has no explicit action; should default to deny.
	if d := Evaluate(cfg, cmd(ir.ShellBash, "docker", "build", ".")); d.Allowed {
		t.Error("docker build should be denied via default action")
	}
}

func TestProgramBasenameMatch(t *testing.T) {
	cfg := mustParse(t, sampleYAML)
	d := Evaluate(cfg, cmd(ir.ShellBash, "/usr/local/go/bin/go", "test"))
	if d.Allowed {
		t.Error("absolute path to go should still match program: go")
	}
}

func TestSubcommandGate(t *testing.T) {
	cfg := mustParse(t, sampleYAML)
	// go vet is not the `test` subcommand → allowed.
	if d := Evaluate(cfg, cmd(ir.ShellBash, "go", "vet")); !d.Allowed {
		t.Error("go vet should be allowed")
	}
}

func TestNestedCommandTriggersDeny(t *testing.T) {
	cfg := mustParse(t, sampleYAML)
	script := &ir.Script{
		Shell: ir.ShellBash,
		Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{
			Argv:   []string{"echo"},
			Nested: []*ir.Script{cmd(ir.ShellBash, "go", "test")},
		}}}},
	}
	if d := Evaluate(cfg, script); d.Allowed {
		t.Error("go test nested in a substitution should be denied")
	}
}

func TestShellRestriction(t *testing.T) {
	y := `
version: 1
rules:
  - id: cmd-only
    match: { command: foo, shells: [cmd] }
    message: "no foo on cmd"
`
	cfg := mustParse(t, y)
	if d := Evaluate(cfg, cmd(ir.ShellBash, "foo")); !d.Allowed {
		t.Error("foo on bash should be allowed (rule is cmd-only)")
	}
	if d := Evaluate(cfg, cmd(ir.ShellCmd, "foo")); d.Allowed {
		t.Error("foo on cmd should be denied")
	}
}

func TestCommandPatternForms(t *testing.T) {
	cfg := mustParse(t, sampleYAML)

	// command + arg: prefix match, trailing args allowed.
	if Evaluate(cfg, cmd(ir.ShellBash, "go", "test", "-run", "X", "./...")).Allowed {
		t.Error("`go test -run X ./...` should match [go, test] prefix")
	}
	// command + flag from the string form "sh -c".
	d := Evaluate(cfg, cmd(ir.ShellBash, "sh", "-c", "rm -rf /"))
	if d.Allowed || d.Rule.ID != "no-shell-wrapper" {
		t.Errorf("`sh -c …` should match the sh-wrapper rule, got %+v", d.Rule)
	}
	// prefix must match positionally: `sh -e` is not `sh -c`.
	if !Evaluate(cfg, cmd(ir.ShellBash, "sh", "-e", "script")).Allowed {
		t.Error("`sh -e` should not match `sh -c`")
	}
	// bare command + refinement: docker with build anywhere in args.
	if Evaluate(cfg, cmd(ir.ShellBash, "docker", "buildx", "build", ".")).Allowed {
		t.Error("docker buildx … should match command:docker + args_any:[build,buildx]")
	}
	// too short for the prefix.
	if !Evaluate(cfg, cmd(ir.ShellBash, "go")).Allowed {
		t.Error("bare `go` should not match the [go, test] rule")
	}
}

func TestOptionsAreOrderIndependent(t *testing.T) {
	// Options match as a set, in any order; positional `push` stays first.
	y := "version: 1\nrules:\n  - id: force-push\n    match: { command: [git, push, --force, --no-verify] }\n    message: x\n"
	cfg := mustParse(t, y)

	orders := [][]string{
		{"git", "push", "--force", "--no-verify"},
		{"git", "push", "--no-verify", "--force"},
		{"git", "push", "origin", "--no-verify", "main", "--force"}, // options interleaved
	}
	for _, argv := range orders {
		if Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("options should match regardless of order: %v", argv)
		}
	}
	// missing one option → no match.
	if !Evaluate(cfg, cmd(ir.ShellBash, "git", "push", "--force")).Allowed {
		t.Error("missing --no-verify should not match")
	}
	// positional must still come first: a different first operand → no match.
	if !Evaluate(cfg, cmd(ir.ShellBash, "git", "stash", "--force", "--no-verify")).Allowed {
		t.Error("wrong positional (stash, not push) should not match")
	}
}

func TestMixedCommandArray(t *testing.T) {
	// A pattern array may interleave options and positionals in any order; the
	// matcher partitions by kind, not by list position.
	y := "version: 1\nrules:\n  - id: docker-debug-build\n    match: { command: [docker, --debug, build] }\n    message: x\n"
	cfg := mustParse(t, y)

	deny := [][]string{
		{"docker", "--debug", "build", "."}, // option before positional in argv
		{"docker", "build", "--debug"},      // positional before option in argv
	}
	for _, argv := range deny {
		if Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("mixed pattern should match %v", argv)
		}
	}
	if !Evaluate(cfg, cmd(ir.ShellBash, "docker", "build")).Allowed {
		t.Error("missing --debug option should not match")
	}
	if !Evaluate(cfg, cmd(ir.ShellBash, "docker", "--debug", "push")).Allowed {
		t.Error("wrong positional (push, not build) should not match")
	}
}

func TestOptionsDoNotConsumePositions(t *testing.T) {
	// An option before the subcommand must not break a positional match.
	cfg := mustParse(t, sampleYAML)
	if Evaluate(cfg, cmd(ir.ShellBash, "go", "--mod=mod", "test", "./...")).Allowed {
		t.Error("`go --mod=mod test` should still match [go, test]")
	}
}

// A value-taking option puts its VALUE among the operands (the matcher can't
// know -C/--context/--prefix consume a word). Positionals match as an ordered
// subsequence, so the real subcommand is still found and the rule still fires —
// closing an easy evasion of a command-gating tool (e.g. `git -C /repo push`).
func TestValueOptionBeforeSubcommandDoesNotEvade(t *testing.T) {
	gitCfg := mustParse(t, "version: 1\nrules:\n  - id: no-force-push\n    match: { command: [git, push, --force] }\n    message: x\n")
	deny := [][]string{
		{"git", "-C", "/repo", "push", "--force"}, // separated value-option before push
		{"git", "-c", "k=v", "push", "--force"},   // -c name=value before push
		{"git", "push", "-C", "/repo", "--force"}, // value-option after push
	}
	for _, argv := range deny {
		if Evaluate(gitCfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("an interposed value-option must not hide the subcommand: %v", argv)
		}
	}

	dockerCfg := mustParse(t, "version: 1\nrules:\n  - id: no-build\n    match: { command: [docker, build] }\n    message: x\n")
	if Evaluate(dockerCfg, cmd(ir.ShellBash, "docker", "--context", "prod", "build", ".")).Allowed {
		t.Error("`docker --context prod build` should match [docker, build]")
	}
}

// Subsequence still requires ORDER: two positionals must appear in the given
// order among the operands, so a reversed operand sequence does not match.
func TestPositionalSubsequenceRespectsOrder(t *testing.T) {
	cfg := mustParse(t, "version: 1\nrules:\n  - id: r\n    match: { command: [git, stash, push] }\n    message: x\n")
	if Evaluate(cfg, cmd(ir.ShellBash, "git", "stash", "push")).Allowed {
		t.Error("`git stash push` should match [git, stash, push]")
	}
	if Evaluate(cfg, cmd(ir.ShellBash, "git", "-C", "/r", "stash", "push")).Allowed {
		t.Error("interposed `-C /r` should not hide the [stash, push] subsequence")
	}
	if !Evaluate(cfg, cmd(ir.ShellBash, "git", "push", "stash")).Allowed {
		t.Error("reversed order (push before stash) must NOT match")
	}
}

func TestCmdSlashOptionsArePortable(t *testing.T) {
	// Under cmd, /c is an option, not a positional path.
	y := "version: 1\nrules:\n  - id: no-cmd-c\n    match: { command: [cmd.exe, /c] }\n    message: x\n"
	cfg := mustParse(t, y)
	if Evaluate(cfg, cmd(ir.ShellCmd, "cmd.exe", "/s", "/c", "dir")).Allowed {
		t.Error("/c should match as an option under cmd, any order")
	}
	// Under a POSIX shell, a leading "/path" is positional, not a flag.
	if !Evaluate(cfg, cmd(ir.ShellBash, "cmd.exe", "/usr/bin/x")).Allowed {
		t.Error("under bash, /usr/bin/x is positional and should not match the /c rule")
	}
}

func TestBareCommandMatchesAnyInvocation(t *testing.T) {
	cfg := mustParse(t, "version: 1\nrules:\n  - id: no-go\n    match: { command: go }\n    message: x\n")
	for _, args := range [][]string{{"go"}, {"go", "build"}, {"go", "test", "./..."}} {
		if Evaluate(cfg, cmd(ir.ShellBash, args...)).Allowed {
			t.Errorf("bare `command: go` should match %v", args)
		}
	}
	if !Evaluate(cfg, cmd(ir.ShellBash, "gofmt", "-w", ".")).Allowed {
		t.Error("`command: go` must not match a different program `gofmt`")
	}
}

// aloof-fog: an allow rule used to match its positionals as an ordered
// SUBSEQUENCE of a command's operands, the same permissive operator used for
// deny rules. That let an allowlisted spelling smuggled into an OPTION'S
// VALUE (not a real subcommand) short-circuit a later deny — fail-open. Allow
// rules now use a position-anchored strict prefix instead (see Match.Command,
// "Allow vs. deny: matching discipline"). These tests pin the fix.

// TestAllowRuleCannotBeSmuggledPastDeny is the canonical red case: with
// `allow: [git, status]` present ahead of a `[git, commit, --no-verify]` deny,
// `git commit -m status --no-verify` used to be Allowed:true because `status`
// (the VALUE of `-m`) counts as an operand and satisfies a one-element
// subsequence. It must now be denied.
func TestAllowRuleCannotBeSmuggledPastDeny(t *testing.T) {
	y := `
version: 1
rules:
  - id: allow-git-status
    match: { command: [git, status] }
    action: allow
  - id: no-force-commit
    match: { command: [git, commit, --no-verify] }
    action: deny
    message: "no --no-verify commits"
`
	cfg := mustParse(t, y)
	d := Evaluate(cfg, cmd(ir.ShellBash, "git", "commit", "-m", "status", "--no-verify"))
	if d.Allowed {
		t.Fatal("`git commit -m status --no-verify` must be denied: `status` is -m's VALUE, not the allowlisted `git status` subcommand")
	}
	if d.Rule == nil || d.Rule.ID != "no-force-commit" {
		t.Fatalf("wrong rule fired: %+v", d.Rule)
	}
}

// TestAllowRuleCannotBeSmuggledPastDeny_WithoutAllowRule is the control: with
// the allow rule absent, the same command is denied regardless (proves the
// deny rule alone already catches it, isolating what the allow rule changes).
func TestAllowRuleCannotBeSmuggledPastDeny_WithoutAllowRule(t *testing.T) {
	y := `
version: 1
rules:
  - id: no-force-commit
    match: { command: [git, commit, --no-verify] }
    action: deny
    message: "no --no-verify commits"
`
	cfg := mustParse(t, y)
	if Evaluate(cfg, cmd(ir.ShellBash, "git", "commit", "-m", "status", "--no-verify")).Allowed {
		t.Fatal("without the allow rule, `git commit -m status --no-verify` should already be denied")
	}
}

// TestAllowRuleStillMatchesPositionally proves the strict-prefix tightening
// didn't break the ordinary case: a genuine `git status` still matches the
// allow rule.
func TestAllowRuleStillMatchesPositionally(t *testing.T) {
	y := `
version: 1
rules:
  - id: allow-git-status
    match: { command: [git, status] }
    action: allow
  - id: deny-git
    match: { command: [git] }
    action: deny
    message: "git is denied by default"
`
	cfg := mustParse(t, y)
	if d := Evaluate(cfg, cmd(ir.ShellBash, "git", "status")); !d.Allowed {
		t.Errorf("`git status` should match the allow rule and clear before the deny-git catch-all, got %+v", d)
	}
}

// TestDenySubsequenceStillPermissive is a regression guard: deny rules must
// keep the permissive ordered-subsequence operand match after allow rules
// switched to strict prefix — the two operators are independent per rule
// action, not a single shared one.
func TestDenySubsequenceStillPermissive(t *testing.T) {
	y := "version: 1\nrules:\n  - id: no-push\n    match: { command: [git, push] }\n    action: deny\n    message: x\n"
	cfg := mustParse(t, y)
	if Evaluate(cfg, cmd(ir.ShellBash, "git", "-C", "/repo", "push")).Allowed {
		t.Error("`git -C /repo push` should still match the [git, push] deny rule (subsequence, not prefix)")
	}
}

// TestAllowRuleValueOptionEscapeHatch documents the recommended authoring for
// an allow rule that must tolerate a leading value-taking option: use
// args_all (program-agnostic, position-insensitive) instead of a positional
// command pattern, since strict prefix on `command: [docker, build]` would
// reject `docker --context prod build` (the option's value `prod` occupies
// operand[0], not `build`).
func TestAllowRuleValueOptionEscapeHatch(t *testing.T) {
	y := `
version: 1
rules:
  - id: allow-docker-build
    match: { command: [docker], args_all: [build] }
    action: allow
  - id: deny-docker
    match: { command: [docker] }
    action: deny
    message: "docker is denied by default"
`
	cfg := mustParse(t, y)
	if d := Evaluate(cfg, cmd(ir.ShellBash, "docker", "--context", "prod", "build")); !d.Allowed {
		t.Errorf("`docker --context prod build` should match the args_all allow rule, got %+v", d)
	}
	// And the strict-prefix positional form would NOT have matched (documents
	// why args_all is the recommended escape hatch, not a positional pattern).
	strictY := `
version: 1
rules:
  - id: allow-docker-build-positional
    match: { command: [docker, build] }
    action: allow
  - id: deny-docker
    match: { command: [docker] }
    action: deny
    message: "docker is denied by default"
`
	strictCfg := mustParse(t, strictY)
	if Evaluate(strictCfg, cmd(ir.ShellBash, "docker", "--context", "prod", "build")).Allowed {
		t.Error("a strict-prefix positional allow rule should NOT match `docker --context prod build`; that's why args_all is the documented escape hatch")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing id":          "version: 1\nrules:\n  - match: { command: go }\n",
		"bad action":          "version: 1\nrules:\n  - id: x\n    action: nuke\n    match: { command: go }\n",
		"empty match":         "version: 1\nrules:\n  - id: x\n    match: {}\n",
		"empty command token": "version: 1\nrules:\n  - id: x\n    match: { command: [go, \"\"] }\n",
		"duplicate id":        "version: 1\nrules:\n  - id: x\n    match: { command: a }\n  - id: x\n    match: { command: b }\n",
		"bad default":         "version: 1\ndefaults: { on_parse_error: maybe }\nrules: []\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// TestEvaluateMatchesNestedCommandsAgainstTheirOwnShell pins U072-F01 /
// U073-F01: a `shells:` scoped rule must be judged against the shell that
// actually OWNS the command being matched, not the top-level script's. A
// nested script (e.g. surfaced by ExpandWrappers unwrapping a `cmd.exe /c
// "del /f …"` wrapper embedded in a bash top-level command) carries its own
// dialect — here ShellCmd — even though the enclosing script is ShellBash.
// Before the fix, Evaluate matched every command, nested or not, against the
// top-level script.Shell, so a rule scoped to `shells: [cmd]` could never
// fire on a dialect that only ever appears nested — a real bypass, not a
// hypothetical one, once wrap.go's expansion starts surfacing nested scripts
// in other dialects.
func TestEvaluateMatchesNestedCommandsAgainstTheirOwnShell(t *testing.T) {
	cfg := mustParse(t, `
version: 1
rules:
  - id: no-del-in-cmd
    match: { command: [del], shells: [cmd] }
    message: "no del"
`)
	top := &ir.Script{
		Shell: ir.ShellBash,
		Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{
			Argv: []string{"cmd.exe", "/c", "del", "/f", "important-file"},
			Nested: []*ir.Script{{
				Shell: ir.ShellCmd,
				Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{
					{Argv: []string{"del", "/f", "important-file"}},
				}}},
			}},
		}}}},
	}
	d := Evaluate(cfg, top)
	if d.Allowed {
		t.Fatal("nested cmd-dialect `del` must be denied by a shells:[cmd] rule, even though the outer script is bash")
	}
}
