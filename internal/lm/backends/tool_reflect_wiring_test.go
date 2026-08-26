package backends

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// reflectHooksFor runs the managed dynamic-hook assembly for a config carrying
// the given tool_reflect_bytes, and returns the resolved post_tool commands.
func reflectHooksFor(t *testing.T, setting int) []string {
	t.Helper()
	cfg := config.NewFixture(config.Fixture{
		Settings: config.SettingsConfig{ToolReflectBytes: setting},
	})
	m := newManagedHooks()
	appendManagedDynamicHooks(m, cfg, t.TempDir(), "", nil)

	var cmds []string
	for _, h := range m.For(bundles.HookEventPostTool) {
		cmds = append(cmds, h.Hook.Command)
	}
	return cmds
}

// hasReflectHook reports whether any command invokes the reflect callback.
func hasReflectHook(cmds []string) bool {
	for _, c := range cmds {
		if strings.Contains(c, "hook tool-reflect") {
			return true
		}
	}
	return false
}

// TestAppendManagedDynamicHooks_InstallsTheReflectHook pins the wiring that
// makes the hook real. Everything else about it can be correct -- the command
// builds, the callback works, the threshold resolves -- and the feature still
// does not exist if nothing puts it on the PostToolUse event.
func TestAppendManagedDynamicHooks_InstallsTheReflectHook(t *testing.T) {
	cmds := reflectHooksFor(t, 0) // unset: enabled at the shared default

	if !hasReflectHook(cmds) {
		t.Fatalf("reflect hook absent from post_tool: %v", cmds)
	}
	if !strings.Contains(strings.Join(cmds, " "), "--min-output-bytes") {
		t.Fatalf("hook installed without its threshold flag, so it would never fire: %v", cmds)
	}
}

// TestAppendManagedDynamicHooks_HonoursDisable pins the other arm. A user who
// disables the hook must not get one: an installed-but-inert hook still runs a
// subprocess on every tool call, which is the cost they were opting out of.
func TestAppendManagedDynamicHooks_HonoursDisable(t *testing.T) {
	if cmds := reflectHooksFor(t, -1); hasReflectHook(cmds) {
		t.Fatalf("reflect hook installed despite being disabled: %v", cmds)
	}
}

// TestAppendManagedDynamicHooks_CarriesTheConfiguredThreshold pins that a
// user-set threshold survives into the installed command rather than the
// default being baked in.
func TestAppendManagedDynamicHooks_CarriesTheConfiguredThreshold(t *testing.T) {
	cmds := reflectHooksFor(t, 777)
	if !strings.Contains(strings.Join(cmds, " "), "--min-output-bytes 777") {
		t.Fatalf("configured threshold did not reach the command: %v", cmds)
	}
}

// TestWireDeclared_ExcludesCtxloomsOwnHooksButKeepsDeclaredOnes pins the
// projection capability-loss reporting depends on, from BOTH sides. Excluding
// everything would silence a real loss a bundle author should hear about;
// excluding nothing re-introduces the "gap nobody asked to use" line for a hook
// ctxloom added on the user's behalf.
func TestWireDeclared_ExcludesCtxloomsOwnHooksButKeepsDeclaredOnes(t *testing.T) {
	m := newManagedHooks()
	m.mergeUnified(
		wire.UnifiedHooks{PostTool: []wire.Hook{{Command: "CTXLOOM_OWN", Type: "command"}}},
		fixedSource(HookSource{Origin: HookOriginContext}))
	m.mergeUnified(
		wire.UnifiedHooks{PostTool: []wire.Hook{{Command: "BUNDLE_DECLARED", Type: "command"}}},
		fixedSource(HookSource{Origin: HookOriginBundle}))

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
		out = append(out, h.Command)
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
