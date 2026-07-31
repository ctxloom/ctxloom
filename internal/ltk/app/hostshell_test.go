package app

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
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
	a.ForceShell = ""
	a.HostShell = "" // what $SHELL=/usr/bin/fish resolves to
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
		t.Fatal("Unanalyzed is set — the dialect substitution has become visible; U075-F01's escalation needs revisiting")
	}
	if r.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", r.ParseError)
	}
}
