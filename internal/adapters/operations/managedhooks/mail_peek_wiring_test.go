package managedhooks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestAppendManagedDynamicHooks_InstallsTheMailPeekOnPostToolForEveryReader:
// mid-turn mail rides the post_tool event — the one moment inside a turn a
// hook can put anything in front of the model — for BOTH readers. Unlike the
// turn-start drain it is not the owner's alone: a runner-fed child's mail-peek
// reads nothing itself (it asks its runner, the spool's one reader), so it
// cannot deliver twice, and a child in a long turn is exactly who needs it.
// It is delivered, not only declared.
//
// MUTATION — gate the mail-peek block on MailByHook, or drop it — turns this
// red.
func TestAppendManagedDynamicHooks_InstallsTheMailPeekOnPostToolForEveryReader(t *testing.T) {
	for _, reader := range []sessions.MailReader{sessions.MailByHook, sessions.MailByRunner} {
		m := newHooks()
		appendManagedDynamicHooks(m, config.NewFixture(config.Fixture{}), nil, reader)
		var cmds []string
		for _, h := range m.For(bundles.HookEventPostTool) {
			cmds = append(cmds, strings.Join(append([]string{h.Hook.Command}, h.Hook.Args...), " "))
		}
		assert.Contains(t, strings.Join(cmds, " "), "hook mail-peek", "reader %d", reader)
		assert.Contains(t, strings.Join(wireCommandsOf(m.Wire().Unified.PostTool), " "), "hook mail-peek", "delivered to the engine, reader %d", reader)
	}
}
