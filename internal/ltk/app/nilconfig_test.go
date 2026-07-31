package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
)

// A nil Config is an ALLOW-ALL config, and must be handled the same way at
// every reader in this type. rules.Evaluate and rules.EvaluatePath both take
// that position explicitly (nil cfg -> Decision{Allowed: true}), and
// denyOnUnanalyzable guards for it; resolveShell and decide did not, so a nil
// Config left through the recover boundary wearing the panic handler's
// "this is an ltk bug, please report it" banner and an Unanalyzed response —
// a decision that could not be checked, dressed up as a crash.
func TestDecide_NilConfigIsAllowAllNotAnInternalError(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  engine.Request
	}{
		{"command", engine.Request{Command: "rm -rf /tmp/whatever"}},
		{"file path", engine.Request{FilePath: "/etc/passwd"}},
		{"empty command", engine.Request{Command: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var warn strings.Builder
			a := New(nil, Shells{})
			a.Warn = &warn

			resp := a.Decide(context.Background(), tc.req)

			if !resp.Allow {
				t.Errorf("nil config denied %+v: %q", tc.req, resp.Reason)
			}
			if resp.Unanalyzed {
				t.Errorf("nil config reported the decision as unanalyzed (ParseError=%q)", resp.ParseError)
			}
			if warn.String() != "" {
				t.Errorf("nil config was reported as an ltk bug on the warn stream: %q", warn.String())
			}
		})
	}
}

// The Config field is exported and assignable, so the same discipline has to
// hold when it is cleared after construction, not only when New is handed nil.
func TestDecide_ConfigClearedAfterConstruction(t *testing.T) {
	var warn strings.Builder
	a := New(nil, Shells{})
	a.Warn = &warn
	a.Config = nil

	resp := a.Decide(context.Background(), engine.Request{Command: "git push --force"})
	if !resp.Allow || resp.Unanalyzed || warn.String() != "" {
		t.Errorf("cleared Config: allow=%v unanalyzed=%v warn=%q", resp.Allow, resp.Unanalyzed, warn.String())
	}
}

// resolveShell is the reader the panic actually came from; pin it directly so
// the guard cannot be removed there while decide keeps its own.
func TestResolveShell_NilConfigFallsThroughToTheDefaults(t *testing.T) {
	a := New(nil, Shells{})
	a.Config = nil
	a.hostShell = ""
	if got := a.resolveShell(""); got != a.DefaultShell {
		t.Errorf("resolveShell with a nil Config = %q, want the default %q", got, a.DefaultShell)
	}
	a.hostShell = "zsh"
	if got := a.resolveShell(""); got != a.hostShell {
		t.Errorf("resolveShell with a nil Config = %q, want the host shell %q", got, a.hostShell)
	}
}
