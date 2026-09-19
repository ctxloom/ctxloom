package agent

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestRouteUnifiedHooks_UnsupportedKindWarns pins the root cause: an agent
// with no native event for a unified hook kind used to express that by
// OMITTING the route from its table, which is indistinguishable from forgetting
// it — the hooks were written nowhere and warned nowhere, and the user's
// configured hook was simply inert. Declaring the gap makes the drop deliberate
// AND visible.
//
// RouteUnifiedHooks reports a Once finding; the dedup is the sink's, so the
// Findings a test collects carry every occurrence.
func TestRouteUnifiedHooks_UnsupportedKindWarns(t *testing.T) {
	var found report.Findings
	var emitted []string
	RouteUnifiedHooks(report.To(&found), "testengine", []HookRoute{
		{Hooks: []wire.Hook{{Command: "a"}}, Event: "PreToolUse"},
		{Hooks: []wire.Hook{{Command: "b"}}, Kind: "session_end", Unsupported: "testengine has no session-end event"},
	}, func(event string, _ wire.Hook) {
		emitted = append(emitted, event)
	})

	if len(emitted) != 1 || emitted[0] != "PreToolUse" {
		t.Fatalf("only the routable hook may be emitted, got %v", emitted)
	}
	out := strings.Join(found.Texts(), "\n")
	for _, want := range []string{"testengine", "session_end"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning must name %q; got %q", want, out)
		}
	}
}

// An unsupported route with NO hooks configured says nothing: the user did not
// ask for the kind, so there is nothing to be silent about. This is the
// discriminator that keeps every launch from warning about every gap.
func TestRouteUnifiedHooks_UnsupportedKindSilentWhenUnused(t *testing.T) {
	var found report.Findings
	RouteUnifiedHooks(report.To(&found), "quietengine", []HookRoute{
		{Kind: "session_end", Unsupported: "quietengine has no session-end event"},
	}, func(string, wire.Hook) { t.Error("nothing to emit") })

	if len(found) != 0 {
		t.Errorf("an unused unsupported route must stay quiet, got %v", found.Texts())
	}
}
