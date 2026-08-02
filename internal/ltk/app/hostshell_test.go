package app

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/shellenv"
)

// A login shell ltk has no frontend for — fish, nu, tcsh, csh, elvish, xonsh —
// resolves through shellenv.ShellFromPath to the empty Shell, which is the
// SAME value an unset $SHELL produces (see shellenv.TestShellFromPath, where
// "/usr/bin/fish" and "" share the "" expectation). resolveShell therefore
// falls through to DefaultShell and the command is analyzed as bash.
//
// This test states the part that is easy to lose: the substitution leaves no
// trace on the decision. Unanalyzed stays false, so the Response an adapter
// encodes is byte-identical to one produced by a dialect ltk actually parses,
// and neither the model nor the operator is told a guess was made.
//
// It is deliberately a characterization pin, not a fix. Every way of acting on
// the distinction crosses a boundary this wave may not cross on its own:
// setting Unanalyzed converts today's correct denials into allows under the
// DEFAULT on_parse_error: allow, and routing a diagnostic anywhere a user can
// see it needs a Warnings-shaped field on engine.Response plus every
// Adapter.Encode — which App.Warn's own doc comment names as an escalation.
// If either becomes the chosen behaviour, this test is the one that must
// change, which is the point of writing it down.
func TestUnrecognizedHostShellIsAnalyzedAsBashWithNoTrace(t *testing.T) {
	a := newApp(t, cfg)

	// The fixture must be hostile from resolveShell's vantage point: nothing
	// else in the precedence chain may supply a dialect, or the fallback under
	// test is never reached.
	a.forceShell = ""
	a.hostShell = "" // what $SHELL=/usr/bin/fish resolves to
	if a.Config.Defaults.Shell != "" {
		t.Fatalf("fixture is not hostile: defaults.shell = %q pre-empts the fallback", a.Config.Defaults.Shell)
	}
	if got := a.resolveShell(""); got != ir.ShellBash {
		t.Fatalf("resolveShell = %q, want the bash fallback", got)
	}

	r := decide(a, "go test ./...")
	if r.Allow {
		t.Fatal("the rule should still fire: the command was analyzed, just as bash")
	}
	if r.Unanalyzed {
		t.Fatal("Unanalyzed is set — the dialect substitution has become visible; the escalation this test warns about needs revisiting")
	}
	if r.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", r.ParseError)
	}
}

// A ksh93 login shell must not be analyzed as mksh. The two are different
// languages: ksh93 has process substitution and C-style `for ((;;))`, mksh has
// neither, and mvdan/sh's MirBSDKorn variant REJECTS both outright. A rejection
// is not a safe default here — under the shipped `on_parse_error: allow` it
// means no rule ever sees the command, so a denied program inside such a
// construct runs with ltk reporting a clean allow.
//
// These are the three constructs measured to diverge across a twelve-case
// sweep; the other nine parse identically under both variants, which is why
// bash is the safe home for ksh93 rather than a guess.
func TestKsh93ConstructsAreStillAnalyzed(t *testing.T) {
	a := newApp(t, cfg)
	a.hostShell = shellenv.ShellFromPath("/bin/ksh93")

	// The fixture must be hostile: the mapping under test is what supplies the
	// dialect, so nothing else in the precedence chain may pre-empt it.
	if a.forceShell != "" || a.Config.Defaults.Shell != "" {
		t.Fatal("fixture is not hostile: another signal pre-empts HostShell")
	}
	if a.hostShell == "" {
		t.Fatal("fixture is not hostile: ksh93 resolved to no shell at all")
	}

	for _, command := range []string{
		`for ((i=0;i<3;i++)); do go test ./...; done`,
		`diff <(go test ./...) <(true)`,
		`go test ./... > >(tee log)`,
	} {
		r := decide(a, command)
		if r.Allow {
			t.Errorf("%q was allowed; the go-test rule never saw it (ParseError=%q)", command, r.ParseError)
		}
	}
}
