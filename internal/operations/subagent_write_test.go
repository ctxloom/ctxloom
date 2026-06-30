package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/subagents"
)

// loadConfigDir writes a minimal config.yaml under a fresh .ctxloom and loads it,
// returning the loaded config and the appDir. The write path (SetSubagent/
// RemoveSubagent) needs a real on-disk config to round-trip through config.Save.
func loadConfigDir(t *testing.T, body string) (*config.Config, string) {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg, appDir
}

// TestSetSubagent_RoundTripsThroughConfig proves the write half persists a
// binding under the `subagents:` key and that a fresh load reads it back — the
// path the agent-assisted setup uses to record the user's choice.
func TestSetSubagent_RoundTripsThroughConfig(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	entry, err := SetSubagent(cfg, SetSubagentRequest{
		Name:     "finder",
		Engine:   "claude-fast",
		Profiles: []string{"p1", "p2"},
	})
	require.NoError(t, err)
	assert.Equal(t, "finder", entry.Name)
	assert.Equal(t, subagents.SourceConfig, entry.Source)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Subagent("finder")
	require.True(t, ok, "set subagent must survive a reload")
	assert.Equal(t, "claude-fast", sub.Engine)
	assert.Equal(t, []string{"p1", "p2"}, sub.Profiles)
}

// TestSetSubagent_UpdatesExisting proves a second set with the same name REPLACES
// the binding (whole-binding rewrite, not a merge).
func TestSetSubagent_UpdatesExisting(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	_, err := SetSubagent(cfg, SetSubagentRequest{Name: "dev", Engine: "a", Profiles: []string{"x"}})
	require.NoError(t, err)
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, err = SetSubagent(reloaded, SetSubagentRequest{Name: "dev", Engine: "b", Profiles: []string{"y", "z"}})
	require.NoError(t, err)

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := final.Subagent("dev")
	require.True(t, ok)
	assert.Equal(t, "b", sub.Engine, "engine replaced")
	assert.Equal(t, []string{"y", "z"}, sub.Profiles, "profiles replaced, not unioned")
}

// TestSetSubagent_EmptyName errors rather than writing a nameless binding.
func TestSetSubagent_EmptyName(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 5\n")
	_, err := SetSubagent(cfg, SetSubagentRequest{Name: "", Profiles: []string{"p"}})
	assert.Error(t, err)
}

// TestRemoveSubagent_RoundTrips proves remove deletes the config-key entry and
// persists the removal.
func TestRemoveSubagent_RoundTrips(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	_, err := SetSubagent(cfg, SetSubagentRequest{Name: "finder", Profiles: []string{"p1"}})
	require.NoError(t, err)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	require.NoError(t, RemoveSubagent(reloaded, "finder"))

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := final.Subagent("finder")
	assert.False(t, ok, "removed subagent must be gone after reload")
}

// TestRemoveSubagent_NotFound errors on an unknown name.
func TestRemoveSubagent_NotFound(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 5\n")
	assert.Error(t, RemoveSubagent(cfg, "nope"))
}

// TestRemoveSubagent_DirectorySourceRefused proves a directory-defined subagent
// is NOT removable via the config-key write path (it is its own file): remove
// errors with a clear pointer rather than silently no-op'ing.
func TestRemoveSubagent_DirectorySourceRefused(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	writeFile(t, filepath.Join(appDir, "subagents", "filed.yaml"), "engine: x\nprofiles: [p]\n")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.Subagent("filed")
	require.True(t, ok, "directory subagent should be visible")

	err = RemoveSubagent(reloaded, "filed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filed.yaml", "error must point at the file to delete")
}

// --- the SessionStart detection nudge ------------------------------------

// TestSubagentSetupNudge_FiresOnlyWhenProfilesPresentNoSubagents pins the exact
// trigger condition: profiles present AND zero subagents → nudge; any other
// combination → silent.
func TestSubagentSetupNudge_FiresOnlyWhenProfilesPresentNoSubagents(t *testing.T) {
	appDir := func() string { return filepath.Join(t.TempDir(), ".ctxloom") }

	t.Run("profiles present, no subagents → nudge", func(t *testing.T) {
		cfg := &config.Config{
			AppPaths: []string{appDir()},
			Profiles: config.ProfilesConfig{Defaults: []string{"default"}},
		}
		assert.NotEmpty(t, SubagentSetupNudge(cfg))
	})

	t.Run("profiles present, subagent configured → silent", func(t *testing.T) {
		cfg := &config.Config{
			AppPaths:  []string{appDir()},
			Profiles:  config.ProfilesConfig{Defaults: []string{"default"}},
			Subagents: map[string]subagents.Subagent{"dev": {Profiles: []string{"default"}}},
		}
		assert.Empty(t, SubagentSetupNudge(cfg), "any subagent silences the nudge")
	})

	t.Run("no profiles, no subagents → silent", func(t *testing.T) {
		cfg := &config.Config{AppPaths: []string{appDir()}}
		assert.Empty(t, SubagentSetupNudge(cfg), "nothing to bind → no nudge")
	})

	t.Run("nil config → silent", func(t *testing.T) {
		assert.Empty(t, SubagentSetupNudge(nil))
	})
}

// TestSubagentSetupNudge_InlineDefinitionsCountAsProfiles proves the
// profiles-present check also counts inline profile definitions (not just
// defaults), so a project that configured profiles inline still gets nudged.
func TestSubagentSetupNudge_InlineDefinitionsCountAsProfiles(t *testing.T) {
	cfg := &config.Config{
		AppPaths: []string{filepath.Join(t.TempDir(), ".ctxloom")},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{"x": {}},
		},
	}
	assert.NotEmpty(t, SubagentSetupNudge(cfg))
}
