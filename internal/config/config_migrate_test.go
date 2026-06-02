package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// runConfigUpgrades drives the real config pipeline over raw bytes, mirroring
// what loadConfigFile does on load.
func runConfigUpgrades(in string) (root map[string]any, applied []string) {
	out, applied := configUpgrades.Run([]byte(in))
	_ = yaml.Unmarshal(out, &root)
	return root, applied
}

func TestConfigUpgrades_LLMRename(t *testing.T) {
	t.Run("moves defaults.llm_plugin to llm.default", func(t *testing.T) {
		root, applied := runConfigUpgrades("defaults:\n  llm_plugin: gemini\n  use_distilled: true\n")
		require.NotEmpty(t, applied)

		llm := root["llm"].(map[string]any)
		assert.Equal(t, "gemini", llm["default"])
		defaults := root["defaults"].(map[string]any)
		assert.NotContains(t, defaults, "llm_plugin")
		assert.Equal(t, true, defaults["use_distilled"], "other defaults preserved")
	})

	t.Run("drops empty defaults block after migrating sole key", func(t *testing.T) {
		root, applied := runConfigUpgrades("defaults:\n  llm_plugin: claude-code\n")
		require.NotEmpty(t, applied)
		assert.NotContains(t, root, "defaults")
		assert.Equal(t, "claude-code", root["llm"].(map[string]any)["default"])
	})

	t.Run("renames llm.plugins to llm.configs", func(t *testing.T) {
		root, applied := runConfigUpgrades("llm:\n  plugins:\n    claude-code: {}\n    gemini:\n      model: pro\n")
		require.NotEmpty(t, applied)
		llm := root["llm"].(map[string]any)
		assert.NotContains(t, llm, "plugins")
		configs := llm["configs"].(map[string]any)
		assert.Contains(t, configs, "claude-code")
		assert.Equal(t, "pro", configs["gemini"].(map[string]any)["model"])
	})

	t.Run("does not clobber an explicit llm.default", func(t *testing.T) {
		root, applied := runConfigUpgrades("llm:\n  default: codex\ndefaults:\n  llm_plugin: gemini\n")
		require.NotEmpty(t, applied)
		assert.Equal(t, "codex", root["llm"].(map[string]any)["default"])
	})

	t.Run("preserves comments and 2-space indent", func(t *testing.T) {
		in := "# top comment\nllm:\n  plugins:\n    claude-code: {}\ndefaults:\n  # keep me\n  use_distilled: true\n  llm_plugin: gemini\n"
		out, applied := configUpgrades.Run([]byte(in))
		require.NotEmpty(t, applied)
		assert.Contains(t, string(out), "# top comment")
		assert.Contains(t, string(out), "# keep me")
		assert.NotContains(t, string(out), "llm_plugin")
		assert.NotContains(t, string(out), "plugins:")
	})
}

func TestConfigUpgrades_StampsCurrentVersion(t *testing.T) {
	// Even an already-key-correct but unversioned config upgrades by gaining the
	// current schema version stamp.
	out, applied := configUpgrades.Run([]byte("llm:\n  default: claude-code\n"))
	require.NotEmpty(t, applied, "unversioned config must upgrade (stamp version)")
	assert.Contains(t, string(out), "version: 2")

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(out, &root))
	assert.Equal(t, CurrentConfigVersion, root["version"])
}

func TestConfigUpgrades_NoOpWhenCurrent(t *testing.T) {
	// A config already at the current version is returned verbatim (no rewrite).
	in := []byte("version: 2\nllm:\n  default: claude-code\n")
	out, applied := configUpgrades.Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, string(in), string(out), "current config must not be reserialized")
}

func TestConfigUpgrades_MalformedYAMLUnchanged(t *testing.T) {
	in := []byte("llm: [unterminated\n")
	out, applied := configUpgrades.Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
}
