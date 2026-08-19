// Package fromv1_test proves the migration OFF config version 1 end to end, through
// the exported load path a user actually takes.
//
// It lives in this directory rather than in config's own test files so that
// retiring support for v1 deletes the migration and its proof together — the
// directory is the unit of support.
package fromv1_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// runConfigUpgrades writes in as this project's config.yaml and drives the REAL
// public load path over it, returning the upgraded document and the names of
// the steps that fired.
//
// It goes through config.Load rather than reaching for the pipeline directly
// for two reasons. This package is imported BY config, so it cannot import it
// back except from an external test package like this one — and driving the
// exported entry point is what makes this a proof that the migration a USER
// gets actually works, rather than a proof that a function this test hand-wired
// works. The upgraded bytes are read back off the pending upgrade, which is the
// same value the interactive rewrite prompt persists, so byte-level assertions
// (comments, indentation) stay available.
func runConfigUpgrades(t *testing.T, in string) (root map[string]any, applied []string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	pending := cfg.GetPendingUpgrade()
	if pending == nil {
		return nil, nil
	}
	require.NoError(t, yaml.Unmarshal(pending.Data, &root))
	return root, pending.Applied
}

// upgradedBytes is runConfigUpgrades for an assertion about the FILE rather
// than the parsed shape: comment and indent preservation. Returns the input
// verbatim when no step fired, which is what "unchanged" means on disk.
func upgradedBytes(t *testing.T, in string) (out []byte, applied []string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	if pending := cfg.GetPendingUpgrade(); pending != nil {
		return pending.Data, pending.Applied
	}
	return []byte(in), nil
}

// llmMap is a typed accessor for root["llm"] in a parsed upgraded config.
func llmMap(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	llm, ok := root["llm"].(map[string]any)
	require.True(t, ok, "llm should be a mapping")
	return llm
}

// The v1→v2 rename now runs as the first stage of the full pipeline; these
// assertions check the v3 end-state the whole chain produces.
func TestConfigUpgrades_LLMRename(t *testing.T) {
	t.Run("moves defaults.llm_plugin to llm.defaults.primary", func(t *testing.T) {
		root, applied := runConfigUpgrades(t, "defaults:\n  llm_plugin: antigravity\n  use_distilled: true\n")
		require.NotEmpty(t, applied)

		llm := llmMap(t, root)
		defaults := llm["defaults"].(map[string]any)
		assert.Equal(t, "antigravity", defaults["primary"])
		// use_distilled moved into the config block; defaults is gone.
		assert.NotContains(t, root, "defaults")
		assert.Equal(t, true, root["config"].(map[string]any)["use_distilled"])
	})

	t.Run("renames llm.plugins to llm.configs and stamps type", func(t *testing.T) {
		root, applied := runConfigUpgrades(t, "llm:\n  plugins:\n    claude-code: {}\n    antigravity:\n      model: pro\n")
		require.NotEmpty(t, applied)
		llm := llmMap(t, root)
		assert.NotContains(t, llm, "plugins")
		configs := llm["configs"].(map[string]any)
		assert.Equal(t, "claude-code", configs["claude-code"].(map[string]any)["type"])
		antigravity := configs["antigravity"].(map[string]any)
		assert.Equal(t, "antigravity", antigravity["type"])
		assert.Equal(t, "pro", antigravity["model"])
	})

	t.Run("preserves comments and 2-space indent", func(t *testing.T) {
		in := "# top comment\nllm:\n  plugins:\n    claude-code: {}\ndefaults:\n  # keep me\n  use_distilled: true\n  llm_plugin: antigravity\n"
		out, applied := upgradedBytes(t, in)
		require.NotEmpty(t, applied)
		assert.Contains(t, string(out), "# top comment")
		assert.Contains(t, string(out), "# keep me")
		assert.NotContains(t, string(out), "llm_plugin")
		assert.NotContains(t, string(out), "plugins:")
	})
}

// TestConfigUpgrades_V2toV3 covers every move the v2→v3 step makes.

// loadFrom writes in as the project config and returns the loaded *Config, for
// assertions about the RESULTING configuration rather than the upgraded bytes.
func loadFrom(t *testing.T, in string) *config.Config {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg
}

// A v1-era document is carried all the way to the current schema IN MEMORY, and
// the file on disk is left exactly as the user wrote it. The non-destructive
// half matters as much as the migration: a load that silently rewrote the file
// would edit a document the user never asked anyone to touch, which is why the
// rewrite is offered through the pending-upgrade prompt instead.
func TestLoad_UpgradesLegacyLLMKeysInMemory(t *testing.T) {
	legacy := "# my config\n" +
		"llm:\n  plugins:\n    claude-code:\n      model: opus\n" +
		"defaults:\n  profiles:\n    - test\n  llm_plugin: antigravity\n"

	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	cfgPath := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, cfgPath, []byte(legacy), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	lm := cfg.GetLMConfig()
	assert.Equal(t, "antigravity", lm.Defaults.Primary, "llm_plugin → llm.defaults.primary")
	require.Contains(t, lm.Configs, "claude-code", "llm.plugins → llm.configs")
	assert.Equal(t, "claude-code", lm.Configs["claude-code"].Type, "v3 adds the type discriminator")
	assert.Equal(t, "opus", lm.Configs["claude-code"].Body["model"])
	assert.Equal(t, "default", cfg.GetDefaultAgent(), "default_agent is set to the synthesized agent")
	assert.Equal(t, []string{"test"}, cfg.DefaultAgentProfiles(), "defaults.profiles → default agent profiles")
	assert.Empty(t, cfg.GetWarnings(), "upgraded config should not produce validation warnings")

	onDisk, err := afero.ReadFile(fs, cfgPath)
	require.NoError(t, err)
	assert.Equal(t, legacy, string(onDisk), "Load must not rewrite the file")
}

// The pre-versioning generation (no `version:` at all) runs the whole upgrade
// pipeline; it too must land clean. Strictness is applied AFTER migration, so a
// key the migrator upgrades forward must not also be reported as a problem.
func TestLoad_UnversionedWithMigratableKey_MigratesWithoutWarning(t *testing.T) {
	cfg := loadFrom(t, "profiles:\n  defaults:\n    - dev\n")

	assert.Empty(t, cfg.GetWarnings(), "an unversioned config must migrate silently: %+v", cfg.GetWarnings())
	assert.Equal(t, []string{"dev"}, cfg.DefaultAgentProfiles())
}
