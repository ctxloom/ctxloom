package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestRouteUnifiedHooks_UnsupportedKindWarns pins the root cause: an agent
// with no native event for a unified hook kind used to express that by
// OMITTING the route from its table, which is indistinguishable from forgetting
// it — the hooks were written nowhere and warned nowhere, and the user's
// configured hook was simply inert. Declaring the gap makes the drop deliberate
// AND visible.
//
// RouteUnifiedHooks warns through clidiag.WarnOnce, whose dedup memory is
// process-wide and never expires — by design, so a warning fired once at
// startup doesn't spam on every later hit. That means this test's own
// warning is only observable the FIRST time its process ever emits it: under
// `go test -count=2` (which reuses one process for the whole package), the
// second run's byte-identical warning is deduped away by the first run's
// memory, and the sink-content assertions below see nothing. ResetWarnOnce
// clears that memory so each run of this test starts clean.
func TestRouteUnifiedHooks_UnsupportedKindWarns(t *testing.T) {
	clidiag.ResetWarnOnce()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	var emitted []string
	RouteUnifiedHooks("testengine", []HookRoute{
		{Hooks: []wire.Hook{{Command: "a"}}, Event: "PreToolUse"},
		{Hooks: []wire.Hook{{Command: "b"}}, Kind: "session_end", Unsupported: "testengine has no session-end event"},
	}, func(event string, _ wire.Hook) {
		emitted = append(emitted, event)
	})

	if len(emitted) != 1 || emitted[0] != "PreToolUse" {
		t.Fatalf("only the routable hook may be emitted, got %v", emitted)
	}
	out := buf.String()
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
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	RouteUnifiedHooks("quietengine", []HookRoute{
		{Kind: "session_end", Unsupported: "quietengine has no session-end event"},
	}, func(string, wire.Hook) { t.Error("nothing to emit") })

	if buf.Len() != 0 {
		t.Errorf("an unused unsupported route must stay quiet, got %q", buf.String())
	}
}
