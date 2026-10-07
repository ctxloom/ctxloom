package rules

import (
	"errors"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// denyRule wraps one match body in a single deny command rule.
func denyRule(match string) string {
	return "schema_version: 1\nrules:\n  - id: r\n    match: " + match + "\n    message: m\n"
}

// allowRule wraps one match body in a single allow command rule.
func allowRule(match string) string {
	return "schema_version: 1\nrules:\n  - id: r\n    match: " + match + "\n    action: allow\n"
}

func denied(t *testing.T, cfg *Config, shell ir.Shell, argv ...string) bool {
	t.Helper()
	return !Evaluate(cfg, cmd(shell, argv...)).Allowed
}

// Every pattern is anchored to the WHOLE argv element. Without the anchor a
// pattern would match any element merely containing it — the "appears
// anywhere" scan the firewall model forbids.
func TestPatternIsAnchoredToTheWholeElement(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [git, push] }`))
	for _, argv := range [][]string{{"git", "pushx"}, {"git", "xpush"}, {"gitx", "push"}, {"xgit", "push"}} {
		if denied(t, cfg, ir.ShellBash, argv...) {
			t.Errorf("%v must not match [git, push]: patterns are anchored", argv)
		}
	}
	if !denied(t, cfg, ir.ShellBash, "git", "push") {
		t.Error("git push must match [git, push]")
	}
}

// An alternation is wrapped before anchoring: `a|b` anchored naively as ^a|b$
// would match any element starting with a or ending with b.
func TestAlternationIsWrappedBeforeAnchoring(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [git, 'push|fetch'] }`))
	for _, op := range []string{"push", "fetch"} {
		if !denied(t, cfg, ir.ShellBash, "git", op) {
			t.Errorf("git %s must match 'push|fetch'", op)
		}
	}
	for _, op := range []string{"pushfetch", "pushx", "xfetch"} {
		if denied(t, cfg, ir.ShellBash, "git", op) {
			t.Errorf("git %s must not match 'push|fetch'", op)
		}
	}
}

func TestArgsPatternsMatchOptionValuesAndCase(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [docker, system, prune], unless: ['--filter(=.*)?'] }`))
	if denied(t, cfg, ir.ShellBash, "docker", "system", "prune", "--filter=until=1h") {
		t.Error("--filter=until=1h must hit the unless exception")
	}
	if !denied(t, cfg, ir.ShellBash, "docker", "system", "prune", "--filterx") {
		t.Error("--filterx must not hit the anchored exception")
	}

	ci := mustParse(t, denyRule(`{ command: [git, config, '(?i)user\.name', '.*'] }`))
	if !denied(t, ci, ir.ShellBash, "git", "config", "User.Name", "Bob") {
		t.Error("(?i) must make the key case-insensitive")
	}
	if denied(t, ci, ir.ShellBash, "git", "config", "userXname", "Bob") {
		t.Error(`an escaped \. must not match an arbitrary character`)
	}
}

// A trailing '.*' operand requires an operand to exist, whatever its text —
// including the empty string, which is how `git config user.name ""` writes.
func TestTrailingDotStarRequiresAnOperandEvenAnEmptyOne(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [git, config, 'user\.name', '.*'] }`))
	if denied(t, cfg, ir.ShellBash, "git", "config", "user.name") {
		t.Error("the bare read must not match: no operand follows the key")
	}
	if !denied(t, cfg, ir.ShellBash, "git", "config", "user.name", "") {
		t.Error(`an empty value is still a write and must match '.*'`)
	}
	// The arity is positional, so a value-option's value BEFORE the
	// subcommand no longer counts toward it.
	if denied(t, cfg, ir.ShellBash, "git", "-c", "k=v", "config", "user.name") {
		t.Error("git -c k=v config user.name is a read and must not match")
	}
}

func TestExactAlignmentForbidsTrailingOperandsAndOptions(t *testing.T) {
	cfg := mustParse(t, `
schema_version: 1
rules:
  - id: read
    match: { command: [git, config, 'user\.name'], align: exact }
    action: allow
  - id: deny-config
    match: { command: [git, config] }
    message: m
`)
	if denied(t, cfg, ir.ShellBash, "git", "config", "user.name") {
		t.Error("the exact read must be cleared by the exact allow")
	}
	for _, argv := range [][]string{
		{"git", "config", "user.name", "Bob"},
		{"git", "config", "--unset", "user.name"},
		{"git", "config", "user.name", "--add"},
	} {
		if !denied(t, cfg, ir.ShellBash, argv...) {
			t.Errorf("%v must fall through the exact allow to the deny", argv)
		}
	}
}

func TestPrefixAllowStillAnchorsAtOperandZero(t *testing.T) {
	cfg := mustParse(t, `
schema_version: 1
rules:
  - id: ok
    match: { command: [git, 'status|log'] }
    action: allow
  - id: no-git
    match: { command: [git] }
    message: m
`)
	if denied(t, cfg, ir.ShellBash, "git", "status", "-s") {
		t.Error("git status -s must be cleared by the prefix allow")
	}
	if !denied(t, cfg, ir.ShellBash, "git", "commit", "-m", "status") {
		t.Error("a -m value must not be smuggled past the deny by a regex allow")
	}
}

// Under cmd and pwsh the program element is normalised the way Windows
// resolves it: case-insensitive, .exe optional. POSIX matches as written.
func TestProgramIsNormalisedUnderCmdAndPwsh(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [git, push] }`))
	for _, sh := range []ir.Shell{ir.ShellCmd, ir.ShellPwsh} {
		for _, prog := range []string{"GIT.EXE", "Git", `C:\Program Files\Git\bin\git.exe`} {
			if !denied(t, cfg, sh, prog, "push") {
				t.Errorf("%s under %s must match [git, push]", prog, sh)
			}
		}
	}
	if denied(t, cfg, ir.ShellBash, "GIT", "push") {
		t.Error("POSIX program names are case-sensitive")
	}
	if denied(t, cfg, ir.ShellBash, "git.exe", "push") {
		t.Error("POSIX does not strip .exe")
	}
}

func parseErr(t *testing.T, y string) *RuleError {
	t.Helper()
	_, err := Parse([]byte(y))
	if err == nil {
		t.Fatalf("Parse must refuse:\n%s", y)
	}
	var re *RuleError
	if !errors.As(err, &re) {
		t.Fatalf("Parse error must be a *RuleError, got %T: %v", err, err)
	}
	return re
}

func TestInvalidPatternIsRefusedNamingRuleAndField(t *testing.T) {
	re := parseErr(t, denyRule(`{ command: [git, stash], unless: [list, '(?!list)'] }`))
	if !errors.Is(re, ErrInvalidPattern) {
		t.Errorf("want ErrInvalidPattern, got %v", re)
	}
	if re.RuleID != "r" || re.Field != "match.unless[1]" {
		t.Errorf("want rule r field match.unless[1], got %q %q", re.RuleID, re.Field)
	}
}

func TestEmptyPatternIsRefused(t *testing.T) {
	re := parseErr(t, denyRule(`{ command: [git, ''] }`))
	if !errors.Is(re, ErrEmptyPattern) || re.Field != "match.command[1]" {
		t.Errorf("want ErrEmptyPattern at match.command[1], got %v", re)
	}
}

func TestOptionPatternInCommandIsRefused(t *testing.T) {
	for _, m := range []string{`{ command: [git, push, --force] }`, `{ command: [rm, '-r.*'] }`} {
		re := parseErr(t, denyRule(m))
		if !errors.Is(re, ErrOptionInCommand) {
			t.Errorf("%s: want ErrOptionInCommand, got %v", m, re)
		}
	}
	// A lone "-" is an operand (stdin), not an option.
	mustParse(t, denyRule(`{ command: [cat, '-'] }`))
}

func TestArgsPredicatesAreRefusedOnAllow(t *testing.T) {
	for field, m := range map[string]string{
		"match.args_any": `{ command: [docker], args_any: [build] }`,
		"match.args_all": `{ command: [docker], args_all: [build] }`,
	} {
		re := parseErr(t, allowRule(m))
		if !errors.Is(re, ErrArgsOnAllow) || re.Field != field {
			t.Errorf("%s: want ErrArgsOnAllow at %s, got %v", m, field, re)
		}
	}
	// unless on an allow narrows it, which is the safe direction.
	mustParse(t, allowRule(`{ command: [git, status], unless: ['--porcelain'] }`))
}

func TestAlignmentIsValidatedAgainstTheAction(t *testing.T) {
	for _, tc := range []struct {
		y    string
		want error
	}{
		{allowRule(`{ command: [git], align: subsequence }`), ErrAlignmentForAction},
		{denyRule(`{ command: [git], align: prefix }`), ErrAlignmentForAction},
		{denyRule(`{ command: [git], align: exact }`), ErrAlignmentForAction},
		{allowRule(`{ command: [git], align: sideways }`), ErrInvalidAlignment},
		{allowRule(`{ shells: [bash], align: exact }`), ErrAlignmentNeedsCommand},
	} {
		if re := parseErr(t, tc.y); !errors.Is(re, tc.want) {
			t.Errorf("want %v, got %v\n%s", tc.want, re, tc.y)
		}
	}
	mustParse(t, denyRule(`{ command: [git], align: subsequence }`))
	mustParse(t, allowRule(`{ command: [git], align: prefix }`))
}

// A removed field fails the load — no compat path — but the error names the
// replacement, so the fix is one line away.
func TestRemovedFieldErrorNamesTheReplacement(t *testing.T) {
	for _, tc := range []struct {
		match, field, fix string
	}{
		{`{ command: [go, install], unless_arg_contains: ["@"] }`, "match.unless_arg_contains", "unless"},
		{`{ command: [git, config], min_operands: 3 }`, "match.min_operands", "'.*'"},
		{`{ command: "go test" }`, "match.command", "list"},
		{`{ path: [VERSION] }`, "match.path", "path_rules"},
	} {
		re := parseErr(t, denyRule(tc.match))
		if !errors.Is(re, ErrRemovedField) || re.Field != tc.field || re.RuleID != "r" {
			t.Errorf("%s: want ErrRemovedField at %s on rule r, got %v", tc.match, tc.field, re)
			continue
		}
		if !strings.Contains(re.Error(), tc.fix) {
			t.Errorf("%s: error %q must name the replacement %q", tc.match, re.Error(), tc.fix)
		}
	}
}

func TestPathRulesLiveUnderTheirOwnKey(t *testing.T) {
	cfg := mustParse(t, `
schema_version: 1
rules:
  - id: no-force
    match: { command: [git, push], args_any: ['--force|-f'] }
    message: m
path_rules:
  - id: no-version
    match: { path: [VERSION] }
    message: m
`)
	if d := EvaluatePath(cfg, "/p/VERSION"); d.Allowed || d.RuleID != "no-version" {
		t.Errorf("path rule must fire: %+v", d)
	}
	if !denied(t, cfg, ir.ShellBash, "git", "push", "-f") {
		t.Error("command rule must fire")
	}
	// A command condition under path_rules is not a field of a path rule.
	if _, err := Parse([]byte("schema_version: 1\npath_rules:\n  - id: x\n    match: { path: [V], command: [go] }\n    message: m\n")); err == nil {
		t.Error("a path rule carrying command must be refused")
	}
	// Ids are unique across both lists.
	re := parseErr(t, "schema_version: 1\nrules:\n  - id: x\n    match: { command: [go] }\n    message: m\npath_rules:\n  - id: x\n    match: { path: [V] }\n    message: m\n")
	if !errors.Is(re, ErrDuplicateID) {
		t.Errorf("want ErrDuplicateID, got %v", re)
	}
}
