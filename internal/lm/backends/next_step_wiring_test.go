package backends

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// turnEndCommands runs the managed dynamic-hook assembly and returns the
// resolved turn_end commands.
func turnEndCommands(t *testing.T) []string {
	t.Helper()
	m := newManagedHooks()
	appendManagedDynamicHooks(m, config.NewFixture(config.Fixture{}), t.TempDir(), "", nil)

	var cmds []string
	for _, h := range m.For(bundles.HookEventTurnEnd) {
		cmds = append(cmds, h.Hook.Command)
	}
	return cmds
}

// TestAppendManagedDynamicHooks_InstallsTheNextStepHookOnTurnEnd pins the
// wiring that makes the feature real. Everything else can be correct — the
// command builds, the callback captures, the distiller reads the hint — and
// nothing is ever captured if no lifecycle carries the hook.
//
// TurnEnd specifically: session_end is too late (no live agent holds the
// context by then) and is not the same event on every engine.
//
// MUTATION — delete the m.mergeUnified TurnEnd block in
// appendManagedDynamicHooks, or move it to another lifecycle — turns this red.
func TestAppendManagedDynamicHooks_InstallsTheNextStepHookOnTurnEnd(t *testing.T) {
	cmds := turnEndCommands(t)

	joined := strings.Join(cmds, " ")
	if !strings.Contains(joined, "hook next-step") {
		t.Fatalf("next-step hook absent from turn_end, so nothing is ever captured: %v", cmds)
	}
}

// TestAppendManagedDynamicHooks_NextStepIsDeliveredNotOnlyDeclared pins that
// the hook survives into the DELIVERED wire set. WireDeclared deliberately
// drops ctxloom's own hooks from capability-loss reporting; a hook that only
// existed there would be reported about and never written.
//
// MUTATION — attribute the hook to HookOriginBundle, or have Wire() filter
// HookOriginContext — turns this red.
func TestAppendManagedDynamicHooks_NextStepIsDeliveredNotOnlyDeclared(t *testing.T) {
	m := newManagedHooks()
	appendManagedDynamicHooks(m, config.NewFixture(config.Fixture{}), t.TempDir(), "", nil)

	delivered := wireCommandsOf(m.Wire().Unified.TurnEnd)
	if !strings.Contains(strings.Join(delivered, " "), "hook next-step") {
		t.Fatalf("next-step hook is not delivered to the engine: %v", delivered)
	}
	if declared := wireCommandsOf(m.WireDeclared().Unified.TurnEnd); strings.Contains(strings.Join(declared, " "), "hook next-step") {
		t.Fatalf("ctxloom's own hook leaked into the declared set, inviting a capability-loss report for a hook nobody asked for: %v", declared)
	}
}
