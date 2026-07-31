package app

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
)

// mappingCfg makes every field of a rules.Decision observable at once: a deny
// with a message, a suggestion, and confirm-by-repeating with BOTH a window
// and a delay, on a path rule and a command rule alike. A field that the
// Decision -> Response mapping forgets shows up here as a zero value.
const mappingCfg = `
version: 1
rules:
  - id: path-rule
    match: { path: ["secrets/**"] }
    action: deny
    message: "not that file"
    suggest: "edit the template instead"
    mode: confirm
    window_seconds: 45
    delay_seconds: 6
  - id: command-rule
    match: { command: [bash] }
    action: deny
    message: "not that command"
    suggest: "use just"
    mode: confirm
    window_seconds: 45
    delay_seconds: 6
`

// wantMapped is the Response every one of decide's Decision -> Response
// conversions must produce for the rules above.
func wantMapped(t *testing.T, got engine.Response, wantReason, wantSuggest string) {
	t.Helper()
	if got.Allow {
		t.Errorf("Allow = true, want false")
	}
	if got.Reason != wantReason {
		t.Errorf("Reason = %q, want %q", got.Reason, wantReason)
	}
	if got.Suggest != wantSuggest {
		t.Errorf("Suggest = %q, want %q", got.Suggest, wantSuggest)
	}
	if !got.Confirmable {
		t.Errorf("Confirmable = false, want true")
	}
	if got.ConfirmWindowSeconds != 45 {
		t.Errorf("ConfirmWindowSeconds = %d, want 45", got.ConfirmWindowSeconds)
	}
	if got.ConfirmDelaySeconds != 6 {
		t.Errorf("ConfirmDelaySeconds = %d, want 6", got.ConfirmDelaySeconds)
	}
}

// decide converts a rules.Decision into an engine.Response on three separate
// return paths — the path-rule branch, the wrapper-unanalyzed fall-through,
// and the ordinary command evaluation. This pins that all three carry the
// SAME six fields, so collapsing them onto one converter is provably
// behaviour-preserving, and so a seventh field added to Decision cannot be
// wired into one path and forgotten in the other two.
func TestDecide_EveryDecisionPathCarriesTheSameFields(t *testing.T) {
	a := newApp(t, mappingCfg)

	t.Run("path rule", func(t *testing.T) {
		got := a.Decide(context.Background(), engine.Request{FilePath: "secrets/prod.env"})
		wantMapped(t, got, "not that file", "edit the template instead")
	})

	t.Run("command rule", func(t *testing.T) {
		got := a.Decide(context.Background(), engine.Request{ToolName: "Bash", Command: "bash script.sh"})
		wantMapped(t, got, "not that command", "use just")
	})

	// A wrapper whose inner command string will not parse leaves the view of
	// the command incomplete, and the deny still has to be reported in full.
	t.Run("deny reached through an unanalyzed wrapper", func(t *testing.T) {
		got := a.Decide(context.Background(), engine.Request{ToolName: "Bash", Command: `bash -c "if"`})
		wantMapped(t, got, "not that command", "use just")
	})
}

// An ALLOW that no rule matched carries the same shape: the mapping is not
// conditional on the verdict.
func TestDecide_AllowCarriesTheDecisionUnchanged(t *testing.T) {
	a := newApp(t, mappingCfg)
	got := a.Decide(context.Background(), engine.Request{ToolName: "Bash", Command: "ls -la"})
	if !got.Allow || got.Reason != "" || got.Suggest != "" || got.Confirmable ||
		got.ConfirmWindowSeconds != 0 || got.ConfirmDelaySeconds != 0 || got.Unanalyzed {
		t.Errorf("clean allow = %+v, want a zero-valued allow", got)
	}
}
