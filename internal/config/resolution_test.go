package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/wire"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveProfile_HookDedupAcrossEvents pins that the unified-hook dedup key
// carries an event discriminator (and the hook Type/Prompt), so a hook is not
// silently dropped just because the same command+matcher already merged on a
// different lifecycle event, and prompt hooks with an empty Command but
// differing Prompt text are not collapsed.
func TestResolveProfile_HookDedupAcrossEvents(t *testing.T) {
	notify := wire.Hook{Command: "notify.sh", Matcher: "*"}
	promptA := wire.Hook{Type: "prompt", Prompt: "remember A"}
	promptB := wire.Hook{Type: "prompt", Prompt: "remember B"}

	profiles := map[string]Profile{
		"p": {
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					// Same command+matcher on two different lifecycles must both survive.
					SessionStart: []wire.Hook{notify},
					SessionEnd:   []wire.Hook{notify},
					// Distinct prompt hooks (empty Command, same matcher) must not collapse.
					PreTool: []wire.Hook{promptA, promptB},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "p")
	assert.NoError(t, err)
	assert.Len(t, resolved.Hooks.Unified.SessionStart, 1)
	assert.Len(t, resolved.Hooks.Unified.SessionEnd, 1, "same hook on a different event must not be deduped away")
	assert.Len(t, resolved.Hooks.Unified.PreTool, 2, "prompt hooks differing only by Prompt must both survive")
}

// TestResolveProfile_HookDedupWithinEvent pins that genuine duplicates within
// the same event (e.g. via diamond inheritance) are still collapsed to one.
func TestResolveProfile_HookDedupWithinEvent(t *testing.T) {
	notify := wire.Hook{Command: "notify.sh", Matcher: "*"}
	profiles := map[string]Profile{
		"base": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{notify}}}},
		"mid":  {Parents: []string{"base"}},
		"child": {
			Parents: []string{"base", "mid"},
			Hooks:   wire.HooksConfig{Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{notify}}},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	assert.NoError(t, err)
	assert.Len(t, resolved.Hooks.Unified.SessionStart, 1, "identical hooks on the same event still dedup")
}

// TestProfileResolutionAccessors covers the thin profile/settings accessors
// that the run and hook paths read through.
func TestProfileResolutionAccessors(t *testing.T) {
	cfg := &Config{
		defaultAgent: "default",
		agents:       map[string]agents.Agent{"default": {Profiles: []string{"dev", "go"}}},
		profiles:     ProfilesConfig{Definitions: map[string]Profile{"dev": {Description: "development"}}},
	}

	assert.Equal(t, []string{"dev", "go"}, cfg.DefaultAgentProfiles())
}

// TestConfig_ShouldUseDistilled covers the Config-level wrapper (delegating to
// SettingsConfig): unset defaults to true, an explicit false is honored.
func TestConfig_ShouldUseDistilled(t *testing.T) {
	assert.True(t, (&Config{}).ShouldUseDistilled(), "distilled defaults to true when unset")

	off := false
	cfg := &Config{settings: SettingsConfig{UseDistilled: &off}}
	assert.False(t, cfg.ShouldUseDistilled(), "an explicit false must be honored")
}

// TestLoadRemoteBundleSeed_GuardBranches covers the early returns: no app paths
// and an absent/empty lockfile both yield a nil seed (nothing to load).
func TestLoadRemoteBundleSeed_GuardBranches(t *testing.T) {
	t.Run("nil_without_app_paths", func(t *testing.T) {
		assert.Nil(t, remoteBundleSeed(t, &Config{}))
	})

	t.Run("nil_with_empty_lockfile", func(t *testing.T) {
		testsupport.Isolate(t)
		dir := t.TempDir() // real, empty .ctxloom: no remotes, no lockfile
		cfg := &Config{appPaths: []string{dir}}
		assert.Nil(t, remoteBundleSeed(t, cfg), "an empty lockfile means no remote bundles to seed")
	})
}

// TestResolveBundleHooks_BuiltinsUnconditional pinned the always-on path when
// session-bind + plan-stamping shipped as EMBEDDED builtin bundle hooks. S8
// moved that content onto taskloom's own loadout (discovered on PATH, not
// embedded — see TestResolveBundleHooks_IncludesCompanionLoadoutHooks_Gated
// in companion_loadout_test.go), which requires a project directory to seed
// into (companionBundleSeed's AppPaths guard). A truly bare Config (no
// AppPaths — nothing to be an "unconditional" project-level bundle FOR) now
// correctly surfaces no hooks at all, which is what this pins today.
func TestResolveBundleHooks_BuiltinsUnconditional(t *testing.T) {
	hooks := (&Config{}).ResolveBundleHooks(nil)
	assert.Empty(t, hooks.PreTool)
	assert.Empty(t, hooks.PostTool)
	assert.Empty(t, hooks.SessionStart)
	assert.Empty(t, hooks.SessionEnd)
	assert.Empty(t, hooks.PreShell)
	assert.Empty(t, hooks.PostFileEdit, "a bare Config with no project directory has no bundle (embedded or companion) to surface hooks from")
}
