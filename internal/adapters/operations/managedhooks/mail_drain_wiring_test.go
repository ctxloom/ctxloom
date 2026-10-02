package managedhooks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// eventCommands runs the managed dynamic-hook assembly and returns the
// resolved commands for one unified event.
func eventCommands(t *testing.T, event string) []string {
	t.Helper()
	m := newHooks()
	appendManagedDynamicHooks(report.Reporter{}, m, gatedFixture(config.Fixture{}), t.TempDir(), "", nil, engine.Interactive)

	var cmds []string
	for _, h := range m.For(event) {
		cmds = append(cmds, strings.Join(append([]string{h.Hook.Command}, h.Hook.Args...), " "))
	}
	return cmds
}

// TestAppendManagedDynamicHooks_InstallsTheMailDrainHookOnTurnStart pins the
// wiring that makes the owner's mail delivery real: the spool can be perfect
// and the hook can read it perfectly, and the session owner still never sees
// a child's report if no lifecycle event carries the hook.
//
// turn_start specifically: the hook's stdout becomes context of the turn that
// is starting, which is the only moment a prompt-driven owner can be handed
// anything. Declared here — ctxloom's OWN hook management — rather than in
// any bundle: a bundle is content a profile may or may not select, and mail
// delivery is not optional.
//
// MUTATION — delete the m.mergeUnified TurnStart block in
// appendManagedDynamicHooks, or move it to another lifecycle — turns this red.
func TestAppendManagedDynamicHooks_InstallsTheMailDrainHookOnTurnStart(t *testing.T) {
	cmds := eventCommands(t, bundles.HookEventTurnStart)

	if !strings.Contains(strings.Join(cmds, " "), "hook mail-drain") {
		t.Fatalf("mail-drain hook absent from turn_start, so the owner is never handed its mail: %v", cmds)
	}
}

// TestAppendManagedDynamicHooks_MailDrainIsDeliveredNotOnlyDeclared pins that
// the hook survives into the DELIVERED wire set and stays out of the declared
// one that capability-loss reporting reads (see the next-step twin).
func TestAppendManagedDynamicHooks_MailDrainIsDeliveredNotOnlyDeclared(t *testing.T) {
	m := newHooks()
	appendManagedDynamicHooks(report.Reporter{}, m, gatedFixture(config.Fixture{}), t.TempDir(), "", nil, engine.Interactive)

	delivered := wireCommandsOf(m.Wire().Unified.TurnStart)
	if !strings.Contains(strings.Join(delivered, " "), "hook mail-drain") {
		t.Fatalf("mail-drain hook is not delivered to the engine: %v", delivered)
	}
	if declared := wireCommandsOf(m.WireDeclared().Unified.TurnStart); strings.Contains(strings.Join(declared, " "), "hook mail-drain") {
		t.Fatalf("ctxloom's own hook leaked into the declared set, inviting a capability-loss report for a hook nobody asked for: %v", declared)
	}
}

// TestAppendManagedDynamicHooks_MailDrainIsTheOwnersOnly is F4 of row
// worried-chief. A structured session — a delegated child, or any run ctxloom
// drives turn by turn — is handed its mail by its runner AS its turn, and the
// runner consumes the file only once the turn has started. A turn-start
// mail-drain declared for it would claim the same file from its in/ during
// that turn (or, for mail swept before its first turn, during the briefing)
// and hand it a second time, leaving the runner's own consume to find it
// gone. Only the session OWNER — the interactive session, which no turn sink
// feeds — reads its mail through the hook.
// MUTATION — declare mail-drain regardless of mode — turns this red.
func TestAppendManagedDynamicHooks_MailDrainIsTheOwnersOnly(t *testing.T) {
	commandsFor := func(mode engine.Mode) string {
		m := newHooks()
		appendManagedDynamicHooks(report.Reporter{}, m, gatedFixture(config.Fixture{}), t.TempDir(), "", nil, mode)
		var cmds []string
		for _, h := range m.For(bundles.HookEventTurnStart) {
			cmds = append(cmds, h.Hook.Command)
		}
		return strings.Join(cmds, " ")
	}
	assert.Contains(t, commandsFor(engine.Interactive), "hook mail-drain", "the owner reads its mail at turn start")
	assert.NotContains(t, commandsFor(engine.Structured), "hook mail-drain", "a structured run's mail IS its turn; a second reader delivers it twice")
}
