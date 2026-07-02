package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
)

// loadConfigDir writes a minimal config.yaml under a fresh .ctxloom and loads it,
// returning the loaded config and the appDir. The write path (SetAgent/
// RemoveAgent) needs a real on-disk config to round-trip through config.Save.
func loadConfigDir(t *testing.T, body string) (*config.Config, string) {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg, appDir
}

// TestSetAgent_RoundTripsThroughConfig proves the write half persists a
// binding under the `agents:` key and that a fresh load reads it back — the
// path the agent-assisted setup uses to record the user's choice.
func TestSetAgent_RoundTripsThroughConfig(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	entry, err := SetAgent(cfg, SetAgentRequest{
		Name:     "finder",
		Engine:   "claude-fast",
		Profiles: []string{"p1", "p2"},
	})
	require.NoError(t, err)
	assert.Equal(t, "finder", entry.Name)
	assert.Equal(t, agents.SourceConfig, entry.Source)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := reloaded.Agent("finder")
	require.True(t, ok, "set agent must survive a reload")
	assert.Equal(t, "claude-fast", sub.Engine)
	assert.Equal(t, []string{"p1", "p2"}, sub.Profiles)
}

// TestSetAgent_UpdatesExisting proves a second set with the same name REPLACES
// the binding (whole-binding rewrite, not a merge).
func TestSetAgent_UpdatesExisting(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")

	_, err := SetAgent(cfg, SetAgentRequest{Name: "dev", Engine: "a", Profiles: []string{"x"}})
	require.NoError(t, err)
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, err = SetAgent(reloaded, SetAgentRequest{Name: "dev", Engine: "b", Profiles: []string{"y", "z"}})
	require.NoError(t, err)

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	sub, ok := final.Agent("dev")
	require.True(t, ok)
	assert.Equal(t, "b", sub.Engine, "engine replaced")
	assert.Equal(t, []string{"y", "z"}, sub.Profiles, "profiles replaced, not unioned")
}

// TestSetAgent_EmptyName errors rather than writing a nameless binding.
func TestSetAgent_EmptyName(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 5\n")
	_, err := SetAgent(cfg, SetAgentRequest{Name: "", Profiles: []string{"p"}})
	assert.Error(t, err)
}

// TestRemoveAgent_RoundTrips proves remove deletes the config-key entry and
// persists the removal.
func TestRemoveAgent_RoundTrips(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	_, err := SetAgent(cfg, SetAgentRequest{Name: "finder", Profiles: []string{"p1"}})
	require.NoError(t, err)

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	require.NoError(t, RemoveAgent(reloaded, "finder"))

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := final.Agent("finder")
	assert.False(t, ok, "removed agent must be gone after reload")
}

// TestRemoveAgent_NotFound errors on an unknown name.
func TestRemoveAgent_NotFound(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 5\n")
	assert.Error(t, RemoveAgent(cfg, "nope"))
}

// TestRemoveAgent_DirectorySourceRefused proves a directory-defined agent
// is NOT removable via the config-key write path (it is its own file): remove
// errors with a clear pointer rather than silently no-op'ing.
func TestRemoveAgent_DirectorySourceRefused(t *testing.T) {
	_, appDir := loadConfigDir(t, "version: 5\n")
	writeFile(t, filepath.Join(appDir, "agents", "filed.yaml"), "engine: x\nprofiles: [p]\n")

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	_, ok := reloaded.Agent("filed")
	require.True(t, ok, "directory agent should be visible")

	err = RemoveAgent(reloaded, "filed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filed.yaml", "error must point at the file to delete")
}

// --- the SessionStart detection nudge ------------------------------------

// TestAgentSetupNudge_FiresOnlyWhenProfilesPresentNoAgents pins the exact
// trigger condition: profiles present AND zero agents → nudge; any other
// combination → silent.
func TestAgentSetupNudge_FiresOnlyWhenProfilesPresentNoAgents(t *testing.T) {
	appDir := func() string { return filepath.Join(t.TempDir(), ".ctxloom") }

	t.Run("profiles present, no agents → nudge", func(t *testing.T) {
		cfg := &config.Config{
			AppPaths: []string{appDir()},
			Profiles: config.ProfilesConfig{Defaults: []string{"default"}},
		}
		assert.NotEmpty(t, AgentSetupNudge(cfg))
	})

	t.Run("profiles present, agent configured → silent", func(t *testing.T) {
		cfg := &config.Config{
			AppPaths: []string{appDir()},
			Profiles: config.ProfilesConfig{Defaults: []string{"default"}},
			Agents:   map[string]agents.Agent{"dev": {Profiles: []string{"default"}}},
		}
		assert.Empty(t, AgentSetupNudge(cfg), "any agent silences the nudge")
	})

	t.Run("no profiles, no agents → silent", func(t *testing.T) {
		cfg := &config.Config{AppPaths: []string{appDir()}}
		assert.Empty(t, AgentSetupNudge(cfg), "nothing to bind → no nudge")
	})

	t.Run("nil config → silent", func(t *testing.T) {
		assert.Empty(t, AgentSetupNudge(nil))
	})
}

// TestAgentSetupNudge_InlineDefinitionsCountAsProfiles proves the
// profiles-present check also counts inline profile definitions (not just
// defaults), so a project that configured profiles inline still gets nudged.
func TestAgentSetupNudge_InlineDefinitionsCountAsProfiles(t *testing.T) {
	cfg := &config.Config{
		AppPaths: []string{filepath.Join(t.TempDir(), ".ctxloom")},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{"x": {}},
		},
	}
	assert.NotEmpty(t, AgentSetupNudge(cfg))
}
