package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

func newApp(t *testing.T, y string) *App {
	t.Helper()
	cfg, err := rules.Parse([]byte(y))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return New(cfg)
}

const cfg = `
version: 1
rules:
  - id: go-test-to-just
    match: { command: [go, test] }
    action: deny
    message: "Use just test."
    suggest: "just test"
`

func decide(a *App, command string) engine.Response {
	return a.Decide(context.Background(), engine.Request{ToolName: "Bash", Command: command})
}

func TestEndToEndDeny(t *testing.T) {
	a := newApp(t, cfg)
	r := decide(a, "go test ./...")
	if r.Allow {
		t.Fatal("go test should be denied")
	}
	if r.Suggest != "just test" {
		t.Errorf("suggest = %q", r.Suggest)
	}
}

func TestEndToEndDenyViaPipeline(t *testing.T) {
	a := newApp(t, cfg)
	// go test buried in a && chain and a subshell must still be caught.
	if decide(a, "cd x && (go test ./...)").Allow {
		t.Error("go test inside `&&` + subshell should be denied")
	}
}

// TestNestingIsCaught proves the denied `go test` is found however it is wrapped
// — subshells, command/process substitution, backticks, pipelines, sequencing,
// compound commands, backgrounding, and assignments — by parsing real shell.
func TestNestingIsCaught(t *testing.T) {
	a := newApp(t, cfg)
	denied := []string{
		"go test ./...",                  // baseline
		"(go test)",                      // subshell
		"( ( go test ) )",                // nested subshells
		"{ go test; }",                   // brace group
		"cd x && go test",                // && sequence
		"false || go test",               // || sequence
		"echo hi; go test",               // ; sequence
		"go test &",                      // background
		"echo before | go test",          // pipeline member
		"echo $(go test)",                // command substitution
		"echo `go test`",                 // backtick substitution
		"diff <(go test) f",              // process substitution
		"x=$(go test)",                   // assignment via substitution
		"if true; then go test; fi",      // if-compound
		"for f in a b; do go test; done", // for-loop body
		"echo \"$(cd d && (go test))\"",  // deeply nested
	}
	for _, cmd := range denied {
		if decide(a, cmd).Allow {
			t.Errorf("should be DENIED (go test reachable): %q", cmd)
		}
	}

	allowed := []string{
		"go build ./...",   // different subcommand
		"echo go test",     // `go test` are args to echo, not a command
		"echo \"go test\"", // quoted literal, not a command
		"gotest ./...",     // different program (substring, not a match)
		"cd go && test x",  // tokens split across commands, neither is `go test`
	}
	for _, cmd := range allowed {
		if !decide(a, cmd).Allow {
			t.Errorf("should be ALLOWED (no `go test` invocation): %q", cmd)
		}
	}
}

// TestWrapperUnderstanding proves trivial wrappers are re-parsed so a denied
// command can't be smuggled through them.
func TestWrapperUnderstanding(t *testing.T) {
	a := newApp(t, cfg) // denies `go test`
	denied := []string{
		`bash -c "go test"`,         // re-parsed, inner matched
		`sh -c "go test ./..."`,     // sh wrapper
		`eval "go test"`,            // eval
		`cd x && bash -c "go test"`, // wrapper inside a sequence
	}
	for _, c := range denied {
		if decide(a, c).Allow {
			t.Errorf("should be DENIED (wrapper hides `go test`): %q", c)
		}
	}
	// We can't understand a dynamic inner command, so we don't block it.
	if !decide(a, `bash -c "$UNDEFINED_CMD"`).Allow {
		t.Error("dynamic wrapper inner should be allowed (not understood)")
	}
	if !decide(a, `bash -c "go build"`).Allow {
		t.Error("wrapper running an allowed command should be allowed")
	}
}

// TestVariableResolutionEndToEnd proves statically-known variables are resolved
// so the real command is matched (including through a wrapper).
func TestVariableResolutionEndToEnd(t *testing.T) {
	a := newApp(t, cfg) // denies `go test`
	if decide(a, "t=test; go $t").Allow {
		t.Error("`t=test; go $t` should resolve to `go test` and be denied")
	}
	if decide(a, `CMD="go test"; bash -c "$CMD"`).Allow {
		t.Error("`CMD=...; bash -c \"$CMD\"` should resolve then re-parse to `go test`")
	}
}

func TestOnOpaqueIsGone(t *testing.T) {
	// The opacity knob was removed; configuring it is now an unknown-field error.
	if _, err := rules.Parse([]byte("version: 1\ndefaults: { on_opaque: deny }\nrules: []\n")); err == nil {
		t.Error("defaults.on_opaque should now be rejected as an unknown field")
	}
}

func TestEndToEndAllow(t *testing.T) {
	a := newApp(t, cfg)
	if !decide(a, "go build ./...").Allow {
		t.Error("go build should be allowed")
	}
	if !decide(a, "").Allow {
		t.Error("empty command should be allowed")
	}
}

// A command the active frontend cannot parse passes through by default. We use
// a malformed bash command so the test does not depend on PowerShell presence.
func TestUnparseableCommandPassesThrough(t *testing.T) {
	a := newApp(t, cfg) // defaults: on_parse_error = allow
	r := a.Decide(context.Background(), engine.Request{Command: "echo $(", Shell: "bash"})
	if !r.Allow {
		t.Error("unparseable command should pass through under on_parse_error: allow")
	}
}

func TestCmdShellEndToEnd(t *testing.T) {
	a := newApp(t, "version: 1\nrules:\n  - id: no-tag\n    match: { command: [git, tag] }\n    message: use the release pipeline\n")
	// git tag buried in a cmd `&` chain, parsed by the cmd frontend.
	r := a.Decide(context.Background(), engine.Request{Command: "echo hi & git tag v1.0", Shell: "cmd"})
	if r.Allow {
		t.Error("git tag in a cmd & chain should be denied")
	}
	// a different command is allowed.
	if !a.Decide(context.Background(), engine.Request{Command: "dir /s", Shell: "cmd"}).Allow {
		t.Error("dir /s should be allowed")
	}
}

// panicFrontend stands in for any future analysis bug: it claims a shell and
// then panics, exactly as the shell lowerer did on a bare redirection.
type panicFrontend struct{ shell ir.Shell }

func (p panicFrontend) Shells() []ir.Shell { return []ir.Shell{p.shell} }

func (p panicFrontend) Parse(context.Context, ir.Shell, string) (*ir.Script, error) {
	panic("synthetic frontend panic")
}

// A panic anywhere on the analysis path must degrade to FAIL-OPEN with a
// warning, never to a block. ltk is a cooperative redirect, not a sandbox: a
// crash that blocks a valid command is strictly worse than a missed rule,
// because the agent gets a stack trace it cannot act on.
func TestPanicOnAnalysisPathFailsOpenWithWarning(t *testing.T) {
	a := newApp(t, cfg)
	var warn bytes.Buffer
	a.Warn = &warn
	a.Registry.Register(panicFrontend{shell: ir.ShellBash})

	r := a.Decide(context.Background(), engine.Request{ToolName: "Bash", Command: "go test ./...", Shell: ir.ShellBash})

	// Half one: the command is ALLOWED. A recover that silently denies is the
	// same bug wearing a different hat.
	if !r.Allow {
		t.Errorf("panic must fail OPEN; got Allow=false reason=%q", r.Reason)
	}
	// Half two: the human is told. A silent recover hides the bug forever.
	got := warn.String()
	if got == "" {
		t.Fatal("panic must emit a warning; Warn got nothing")
	}
	if !strings.Contains(got, "synthetic frontend panic") {
		t.Errorf("warning = %q, want it to name the panic value", got)
	}
	if !strings.Contains(got, "go test ./...") {
		t.Errorf("warning = %q, want it to name the command that triggered it", got)
	}
}

// The recover must not swallow a legitimate deny. Denials are RETURN VALUES,
// never panics, so the deferred handler may only touch the result when
// recover() actually caught something.
func TestRecoverDoesNotSwallowDenials(t *testing.T) {
	a := newApp(t, cfg)
	var warn bytes.Buffer
	a.Warn = &warn

	r := decide(a, "go test ./...")
	if r.Allow {
		t.Fatal("go test must still be denied with the recover boundary in place")
	}
	if r.Suggest != "just test" || r.Reason == "" {
		t.Errorf("denial detail lost: %+v", r)
	}
	if warn.Len() != 0 {
		t.Errorf("no panic occurred, so nothing should be warned; got %q", warn.String())
	}
}

// A panic under `on_parse_error: deny` follows the operator's explicit policy
// for "ltk could not analyze this" — the same policy a parse error takes — and
// still warns.
func TestPanicHonoursParseErrorDenyPolicy(t *testing.T) {
	a := newApp(t, "version: 1\ndefaults: { on_parse_error: deny }\nrules: []\n")
	var warn bytes.Buffer
	a.Warn = &warn
	a.Registry.Register(panicFrontend{shell: ir.ShellBash})

	r := a.Decide(context.Background(), engine.Request{Command: "echo hi", Shell: ir.ShellBash})
	if r.Allow {
		t.Error("under on_parse_error: deny an unanalyzable command should be denied")
	}
	if warn.Len() == 0 {
		t.Error("a recovered panic must warn regardless of the resulting decision")
	}
}

// A zero App (no New) must not nil-panic on the warning sink.
func TestPanicWithNoWarnSink(t *testing.T) {
	a := newApp(t, cfg)
	a.Warn = nil
	a.Registry.Register(panicFrontend{shell: ir.ShellBash})
	if !a.Decide(context.Background(), engine.Request{Command: "x", Shell: ir.ShellBash}).Allow {
		t.Error("a nil Warn sink must not change the fail-open outcome")
	}
}

func TestParseErrorDenyPolicy(t *testing.T) {
	a := newApp(t, "version: 1\ndefaults: { on_parse_error: deny }\nrules: []\n")
	r := a.Decide(context.Background(), engine.Request{Command: "echo $(", Shell: "bash"})
	if r.Allow {
		t.Error("unparseable command should be denied under on_parse_error: deny")
	}
}
