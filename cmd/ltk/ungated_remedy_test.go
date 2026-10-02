package main

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestUngatedToolDenyReason_FixIsTheFixLine: the deny reason the hook host
// shows the agent is the hook's ONLY channel (evaluate fails closed and never
// returns an error, so RenderError never sees it) — its remedy is still the
// one fix line clifmt.FixLine renders everywhere else, not ad hoc prose.
func TestUngatedToolDenyReason_FixIsTheFixLine(t *testing.T) {
	adapter, err := engine.Get("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	reason := ungatedToolDenyReason(adapter, engine.Request{ToolName: "SmartEdit"})

	fix := clifmt.FixLine("", ungatedToolRemedy("SmartEdit"))
	if !strings.HasSuffix(reason, fix) {
		t.Errorf("the deny reason must end with its fix line %q, got %q", fix, reason)
	}
	if strings.Contains(reason, "To fix") {
		t.Errorf("the remedy must not also ride as prose in the message, got %q", reason)
	}
	for _, want := range []string{"SmartEdit", "manage install"} {
		if !strings.Contains(fix, want) {
			t.Errorf("the fix line must name %q, got %q", want, fix)
		}
	}
}
