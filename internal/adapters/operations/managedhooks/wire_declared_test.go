package managedhooks

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestWireDeclared_ExcludesCtxloomsOwnHooksButKeepsDeclaredOnes pins the
// projection capability-loss reporting depends on, from BOTH sides. Excluding
// everything would silence a real loss a bundle author should hear about;
// excluding nothing re-introduces the "gap nobody asked to use" line for a hook
// ctxloom added on the user's behalf.
func TestWireDeclared_ExcludesCtxloomsOwnHooksButKeepsDeclaredOnes(t *testing.T) {
	m := newHooks()
	m.mergeUnified(
		wire.UnifiedHooks{PostTool: []wire.Hook{{Command: "CTXLOOM_OWN", Type: "command"}}},
		fixedSource(Source{Origin: OriginContext}))
	m.mergeUnified(
		wire.UnifiedHooks{PostTool: []wire.Hook{{Command: "BUNDLE_DECLARED", Type: "command"}}},
		fixedSource(Source{Origin: OriginBundle}))

	declared := wireCommandsOf(m.WireDeclared().Unified.PostTool)
	if hasCommand(declared, "CTXLOOM_OWN") {
		t.Fatalf("ctxloom's own hook leaked into the declared set: %v", declared)
	}
	if !hasCommand(declared, "BUNDLE_DECLARED") {
		t.Fatalf("a bundle-declared hook was dropped, hiding a real loss: %v", declared)
	}

	// Delivery is unaffected: a managed hook a backend CAN carry must still be
	// written. WireDeclared is a reporting projection, not a delivery filter.
	all := wireCommandsOf(m.Wire().Unified.PostTool)
	if !hasCommand(all, "CTXLOOM_OWN") || !hasCommand(all, "BUNDLE_DECLARED") {
		t.Fatalf("Wire lost a hook; delivery must carry both: %v", all)
	}
}

func wireCommandsOf(hooks []wire.Hook) []string {
	out := make([]string, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, strings.Join(append([]string{h.Command}, h.Args...), " "))
	}
	return out
}

func hasCommand(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
