package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestProfileResolutionAccessors covers the thin profile/settings accessors
// that the run and hook paths read through.
func TestProfileResolutionAccessors(t *testing.T) {
	cfg := &Config{Profiles: ProfilesConfig{
		Defaults:    []string{"dev", "go"},
		Definitions: map[string]Profile{"dev": {Description: "development"}},
	}}

	assert.Equal(t, []string{"dev", "go"}, cfg.EffectiveDefaultProfiles())

	t.Run("ProfileDefinition_hit", func(t *testing.T) {
		p, ok := cfg.ProfileDefinition("dev")
		assert.True(t, ok)
		assert.Equal(t, "development", p.Description)
	})

	t.Run("ProfileDefinition_miss", func(t *testing.T) {
		_, ok := cfg.ProfileDefinition("missing")
		assert.False(t, ok)
	})
}

// TestConfig_ShouldUseDistilled covers the Config-level wrapper (delegating to
// SettingsConfig): unset defaults to true, an explicit false is honored.
func TestConfig_ShouldUseDistilled(t *testing.T) {
	assert.True(t, (&Config{}).ShouldUseDistilled(), "distilled defaults to true when unset")

	off := false
	cfg := &Config{Settings: SettingsConfig{UseDistilled: &off}}
	assert.False(t, cfg.ShouldUseDistilled(), "an explicit false must be honored")
}

// TestLoadRemoteBundleSeed_GuardBranches covers the early returns: no app paths
// and an absent/empty lockfile both yield a nil seed (nothing to load).
func TestLoadRemoteBundleSeed_GuardBranches(t *testing.T) {
	t.Run("nil_without_app_paths", func(t *testing.T) {
		assert.Nil(t, (&Config{}).loadRemoteBundleSeed())
	})

	t.Run("nil_with_empty_lockfile", func(t *testing.T) {
		testsupport.Isolate(t)
		dir := t.TempDir() // real, empty .ctxloom: no remotes, no lockfile
		cfg := &Config{AppPaths: []string{dir}}
		assert.Nil(t, cfg.loadRemoteBundleSeed(), "an empty lockfile means no remote bundles to seed")
	})
}

// TestResolveBundleHooks_BuiltinsUnconditional pins the always-on path: even a
// bare config with no profiles surfaces the built-in bundle hooks (session bind
// + plan stamping) shipped embedded in the binary.
func TestResolveBundleHooks_BuiltinsUnconditional(t *testing.T) {
	hooks := (&Config{}).ResolveBundleHooks()

	assert.NotEmpty(t, hooks.SessionStart, "built-in bundles ship a session_start hook")
	assert.NotEmpty(t, hooks.PostFileEdit, "built-in bundles ship a post_file_edit hook")
}
