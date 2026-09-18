package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// TestAppendManagedDynamicHooks_InstallsTheSkillMatesHook pins the wiring that
// makes the skill-mates hook real: it rides the managed PostToolUse set
// beside the reflect hook, ungated, because there is nothing to configure --
// it is silent for every skill outside a link group.
//
// MUTATION -- drop the mergeUnified call for NewSkillMatesHook -- turns this
// red.
func TestAppendManagedDynamicHooks_InstallsTheSkillMatesHook(t *testing.T) {
	m := newManagedHooks()
	appendManagedDynamicHooks(m, config.NewFixture(config.Fixture{}), t.TempDir(), "", nil)

	var matchers []string
	for _, h := range m.For(bundles.HookEventPostTool) {
		if h.Hook.Matcher == "Skill" {
			matchers = append(matchers, h.Hook.Command)
		}
	}
	if assert.Len(t, matchers, 1, "exactly one Skill-matched PostToolUse hook") {
		assert.Contains(t, matchers[0], "hook skill-mates")
	}
}
