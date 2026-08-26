package backends

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
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
