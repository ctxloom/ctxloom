package managedhooks

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// turnEndCommands runs the managed dynamic-hook assembly and returns the
// resolved turn_end commands.
func turnEndCommands(t *testing.T) []string {
	t.Helper()
	m := newHooks()
	appendManagedDynamicHooks(m, config.NewFixture(config.Fixture{}), nil, sessions.MailByHook)

	var cmds []string
	for _, h := range m.For(bundles.HookEventTurnEnd) {
		cmds = append(cmds, strings.Join(append([]string{h.Hook.Command}, h.Hook.Args...), " "))
	}
	return cmds
}

// TestAppendManagedDynamicHooks_InstallsNoTurnEndCallback pins the removal
// of the TurnEnd next-step capture: it fed only session compaction's task
// hint, so ctxloom's managed set carries no turn_end callback at all — and a
// project re-delivered from this set loses the one an older ctxloom wrote.
func TestAppendManagedDynamicHooks_InstallsNoTurnEndCallback(t *testing.T) {
	if cmds := turnEndCommands(t); len(cmds) != 0 {
		t.Fatalf("the managed set carries a turn_end callback: %v", cmds)
	}
}
