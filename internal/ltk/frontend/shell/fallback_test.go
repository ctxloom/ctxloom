package shell_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// degradingCommand is a command line whose expansion FAILS, forcing the
// lowerer onto its literal-text fallback, while every word a rule cares about
// is spelled with double quotes. `${NOPE:?}` on an unset variable is a real
// POSIX construct that makes mvdan.cc/sh's expander return an error rather
// than a value, which is the only door into the fallback path.
const degradingCommand = `git "push" "--force" ${NOPE:?}`

// requireExpansionFails is the §11k guard: it asserts the fixture is hostile
// from the code-under-test's point of view. Without it, a green argv assertion
// could mean "the fallback handles quoting correctly" OR "expansion succeeded
// and the fallback never ran" — and no exit code distinguishes those.
func requireExpansionFails(t *testing.T, src string) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("fixture is not a call expression: %T", file.Stmts[0].Cmd)
	}
	cfg := &expand.Config{Env: expand.FuncEnviron(func(string) string { return "" })}
	if _, err := expand.Fields(cfg, call.Args...); err == nil {
		t.Fatalf("fixture is not hostile: expand.Fields(%q) succeeded, so the literal fallback never runs", src)
	}
}

// TestArgvFallback_KeepsDoubleQuotedWords pins U071-F05. literalFallback used
// to handle only *syntax.Lit and *syntax.SglQuoted, so a *syntax.DblQuoted
// word rendered as "" and argvFallback then dropped the argument entirely.
// The consequence is a silent MIS-PARSE, not a cosmetic one: `push` and
// `--force` vanish from argv, so a deny rule written for exactly this command
// never fires and nothing anywhere says the argv was a guess.
func TestArgvFallback_KeepsDoubleQuotedWords(t *testing.T) {
	requireExpansionFails(t, degradingCommand)

	s, err := shell.New().Parse(context.Background(), ir.ShellBash, degradingCommand)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	cmds := s.Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands = %d, want 1", len(cmds))
	}
	if got, want := cmds[0].Argv, []string{"git", "push", "--force"}; !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

// TestArgvFallback_DenyRuleStillFires is the consequence altitude for
// U071-F05: the point of keeping double-quoted words is that the rule engine
// still sees the command the operator wrote a rule against. Pre-fix this
// allowed, which is a deny-rule bypass reachable from an ordinary command line.
func TestArgvFallback_DenyRuleStillFires(t *testing.T) {
	requireExpansionFails(t, degradingCommand)

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
	s, err := shell.New().Parse(context.Background(), ir.ShellBash, degradingCommand)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if d := rules.Evaluate(cfg, s); d.Allowed {
		t.Errorf("Evaluate allowed %q; the deny rule must still fire on the fallback path", degradingCommand)
	}
}
