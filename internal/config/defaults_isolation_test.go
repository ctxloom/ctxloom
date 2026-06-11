package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergeDefaultConfig_BodyMutationIsNotMistakenForDefault is a regression
// guard for the overlay deep copy: the documented protection ("a later
// in-place registry mutation isn't mistaken for 'still the default' and
// stripped by Save") must hold for Body mutations too. A shallow top-level
// copy shared each entry's Body map with the overlay snapshot, so mutating
// Body["model"] in place also rewrote the snapshot and userAuthoredLM kept
// stripping the (now user-modified) entry.
func TestMergeDefaultConfig_BodyMutationIsNotMistakenForDefault(t *testing.T) {
	cfg := &Config{LM: LMConfig{Configs: map[string]LLMConfig{}}}
	mergeDefaultConfig(cfg)
	require.NotEmpty(t, cfg.LM.Configs, "embedded default registry must overlay an empty user registry")
	require.NotNil(t, cfg.lmDefaultOverlay)

	entry, ok := cfg.LM.Configs["claude-code"]
	require.True(t, ok)
	require.NotNil(t, entry.Body)

	// Mutate the live registry entry's Body IN PLACE (no map reassignment).
	entry.Body["model"] = "user-pinned-model"

	// The overlay snapshot must be unaffected by the mutation...
	assert.NotEqual(t, "user-pinned-model", cfg.lmDefaultOverlay.Configs["claude-code"].Body["model"],
		"overlay snapshot must not share Body storage with the live registry")

	// ...so the mutated entry no longer matches the default and survives the
	// Save-time stripping.
	stripped := cfg.userAuthoredLM()
	got, ok := stripped.Configs["claude-code"]
	require.True(t, ok, "a Body-mutated entry is user configuration and must be persisted")
	assert.Equal(t, "user-pinned-model", got.Body["model"])
}

// TestInheritedDefaults_RunOnly pins the run-only nature of home-inherited
// defaults (ProfilesConfig.SetInheritedDefaults): GetDefaultProfiles falls
// back to them, but they are never explicit and never persisted by Save.
func TestInheritedDefaults_RunOnly(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	cfg := &Config{AppPaths: []string{appDir}, fs: fs}

	cfg.Profiles.SetInheritedDefaults([]string{"home-dev"})

	assert.Equal(t, []string{"home-dev"}, cfg.GetDefaultProfiles(),
		"inherited defaults must drive GetDefaultProfiles")
	assert.Empty(t, cfg.ExplicitDefaultProfiles(),
		"inherited defaults are not explicit — first-profile auto-promotion must not be suppressed")
	assert.True(t, cfg.Profiles.InheritedDefaultsResolved())

	// An unrelated Save must not materialize the inherited defaults.
	require.NoError(t, cfg.Save())
	data, err := afero.ReadFile(fs, filepath.Join(appDir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "home-dev",
		"run-only inherited defaults must never be written to the project config")

	// Explicit defaults still win over inherited.
	cfg.Profiles.AddDefaultProfile("proj-dev")
	assert.Equal(t, []string{"proj-dev"}, cfg.GetDefaultProfiles())
}
