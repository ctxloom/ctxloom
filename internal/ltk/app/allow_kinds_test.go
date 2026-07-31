package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// decide returns Allow=true for four different reasons, and two of them mean
// the opposite of what the third and fourth mean: "the rules were checked and
// nothing objected" versus "nothing could be checked at all". An operator
// running the shipped fail-open policy has to be able to tell those apart, or
// a guard that has stopped guarding looks exactly like a guard with nothing to
// say. Unanalyzed/ParseError are what separate them; this pins that separation
// so the four cannot silently re-merge into a bare Response{Allow: true}.
func TestDecide_AnalyzedAndUnanalyzedAllowsAreDistinguishable(t *testing.T) {
	// "ltk crashed": a frontend that claims a shell and then panics.
	t.Run("crashed", func(t *testing.T) {
		a := newApp(t, cfg)
		a.Warn = io.Discard
		a.Registry.Register(panicFrontend{shell: ir.ShellBash})

		r := a.Decide(context.Background(), engine.Request{ToolName: "Bash", Command: "go test ./..."})
		requireUnanalyzedAllow(t, r)
		if !strings.Contains(r.ParseError, "internal error") {
			t.Errorf("a crash's ParseError = %q, want it to name an internal error", r.ParseError)
		}
	})

	// "could not parse, policy says pass": a dialect with no frontend at all.
	t.Run("unparseable", func(t *testing.T) {
		a := newApp(t, cfg)
		r := a.Decide(context.Background(), engine.Request{
			ToolName: "Bash", Command: "go test ./...", Shell: ir.Shell("fictional-shell")})
		requireUnanalyzedAllow(t, r)
		if strings.Contains(r.ParseError, "internal error") {
			t.Errorf("a parse failure was reported as an internal error: %q", r.ParseError)
		}
	})

	// "no rule matched": the whole command was understood and nothing objected.
	t.Run("no rule matched", func(t *testing.T) {
		a := newApp(t, cfg)
		requireAnalyzedAllow(t, a.Decide(context.Background(),
			engine.Request{ToolName: "Bash", Command: "ls -la"}))
	})

	// "nothing to check": an empty command is fully analyzed, trivially.
	t.Run("nothing to check", func(t *testing.T) {
		a := newApp(t, cfg)
		requireAnalyzedAllow(t, a.Decide(context.Background(),
			engine.Request{ToolName: "Bash", Command: "   "}))
	})
}

func requireUnanalyzedAllow(t *testing.T, r engine.Response) {
	t.Helper()
	if !r.Allow {
		t.Fatalf("want an allow, got a deny: %q", r.Reason)
	}
	if !r.Unanalyzed {
		t.Error("an allow reached without analyzing the command must say so (Unanalyzed)")
	}
	if r.ParseError == "" {
		t.Error("an unanalyzed allow must carry the reason it could not be analyzed")
	}
}

func requireAnalyzedAllow(t *testing.T, r engine.Response) {
	t.Helper()
	if !r.Allow {
		t.Fatalf("want an allow, got a deny: %q", r.Reason)
	}
	if r.Unanalyzed || r.ParseError != "" {
		t.Errorf("a fully analyzed allow was marked unanalyzed: Unanalyzed=%v ParseError=%q",
			r.Unanalyzed, r.ParseError)
	}
}
