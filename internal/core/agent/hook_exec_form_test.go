package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestCtxloomHooks_AreExecForm: every hook ctxloom constructs for itself
// runs in exec form — the ctxloom executable spawned directly with its
// callback's argv, no shell to parse a path or an argument — and carries
// exactly the argv its callback takes.
func TestCtxloomHooks_AreExecForm(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook wire.Hook
		args []string
	}{
		{"inject-context", NewContextInjectionHook("abc"), []string{"hook", "inject-context", "abc"}},
		{"inject-context chunk", NewContextInjectionChunkHook("abc", 2, 3), []string{"hook", "inject-context", "--part", "2", "--of", "3", "abc"}},
		{"tool-reflect", NewToolReflectHook(4096), []string{"hook", "tool-reflect", "--min-output-bytes", "4096"}},
		{"skill-mates", NewSkillMatesHook(), []string{"hook", "skill-mates"}},
		{"next-step", NewNextStepHook(), []string{"hook", "next-step"}},
		{"mail-drain", NewMailDrainHook(), []string{"hook", "mail-drain"}},
		{"permission", ApprovalHook("PermissionRequest", "", time.Minute), []string{"hook", "permission", "--event", "PermissionRequest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, CtxloomCommand(), tc.hook.Command, "the executable alone")
			assert.Equal(t, tc.args, tc.hook.Args)
			assert.Equal(t, "command", tc.hook.Type)
		})
	}
}
