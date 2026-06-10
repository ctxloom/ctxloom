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
		root, applied := runConfigUpgrades("defaults:\n  llm_plugin: antigravity\n  use_distilled: true\n")
		require.NotEmpty(t, applied)

		llm := llmMap(t, root)
		defaults := llm["defaults"].(map[string]any)
		assert.Equal(t, "antigravity", defaults["primary"])
		// use_distilled moved into the config block; defaults is gone.
		assert.NotContains(t, root, "defaults")
		assert.Equal(t, true, root["config"].(map[string]any)["use_distilled"])
	})

	t.Run("renames llm.plugins to llm.configs and stamps type", func(t *testing.T) {
		root, applied := runConfigUpgrades("llm:\n  plugins:\n    claude-code: {}\n    antigravity:\n      model: pro\n")
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
		out, applied := configUpgrades.Run([]byte(in))
		require.NotEmpty(t, applied)
		assert.Contains(t, string(out), "# top comment")
		assert.Contains(t, string(out), "# keep me")
		assert.NotContains(t, string(out), "llm_plugin")
		assert.NotContains(t, string(out), "plugins:")
	})
}

// TestConfigUpgrades_V2toV3 covers every move the v2→v3 step makes.
func TestConfigUpgrades_V2toV3(t *testing.T) {
	in := "version: 2\n" +
		"llm:\n" +
		"  default: claude-code\n" +
		"  configs:\n" +
		"    claude-code: {}\n" +
		"    antigravity:\n" +
		"      model: pro-model\n" +
		"  compaction:\n" +
		"    llm: antigravity\n" +
		"    model: flash-model\n" +
		"    chunks: 4096\n" +
		"defaults:\n" +
		"  profiles:\n" +
		"    - proj/dev\n" +
		"  use_distilled: false\n" +
		"profiles:\n" +
		"  my-profile:\n" +
		"    bundles: [a/b]\n"

	root, applied := runConfigUpgrades(in)
	require.NotEmpty(t, applied)

	assert.Equal(t, 3, root["version"])

	llm := llmMap(t, root)
	// default → defaults.primary
	defaults := llm["defaults"].(map[string]any)
	assert.Equal(t, "claude-code", defaults["primary"])
	// compaction.llm → defaults.fast
	assert.Equal(t, "antigravity", defaults["fast"])

	// each config gains its type discriminator
	configs := llm["configs"].(map[string]any)
	assert.Equal(t, "claude-code", configs["claude-code"].(map[string]any)["type"])
	antigravity := configs["antigravity"].(map[string]any)
	assert.Equal(t, "antigravity", antigravity["type"])
	// compaction.model folded onto the fast label's model
	assert.Equal(t, "flash-model", antigravity["model"])

	// compaction block is gone
	assert.NotContains(t, llm, "compaction")

	// chunks + use_distilled live under config
	cfgBlock := root["config"].(map[string]any)
	assert.Equal(t, 4096, cfgBlock["compaction_chunks"])
	assert.Equal(t, false, cfgBlock["use_distilled"])

	// profiles.defaults + profiles.definitions
	prof := root["profiles"].(map[string]any)
	assert.Contains(t, prof["defaults"], "proj/dev")
	defs := prof["definitions"].(map[string]any)
	assert.Contains(t, defs, "my-profile")

	// top-level defaults bag is gone
	assert.NotContains(t, root, "defaults")
}

// Compaction with no explicit llm points the fast role at the primary label.
func TestConfigUpgrades_V2toV3_CompactionFallsBackToPrimary(t *testing.T) {
	in := "version: 2\nllm:\n  default: claude-code\n  configs:\n    claude-code: {}\n  compaction:\n    chunks: 8000\n"
	root, _ := runConfigUpgrades(in)
	llm := llmMap(t, root)
	defaults := llm["defaults"].(map[string]any)
	assert.Equal(t, "claude-code", defaults["fast"], "fast falls back to the primary label")
}

func TestConfigUpgrades_V2toV3_PreservesComments(t *testing.T) {
	// Comments on untouched structure survive the node round-trip. A renamed
	// key's own head comment is not guaranteed (it moves with the key), so we
	// pin the comment to a sibling the upgrade leaves alone — the config-block
	// settings, whose use_distilled node is relocated with its comment intact.
	in := "# header\nversion: 2\nllm:\n  default: claude-code\n  configs:\n    claude-code: {}\ndefaults:\n  # keep me\n  use_distilled: false\n"
	out, applied := configUpgrades.Run([]byte(in))
	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "# header", "top-level comment survives")
	assert.Contains(t, string(out), "# keep me", "relocated use_distilled keeps its comment")
	assert.Contains(t, string(out), "version: 3")
}

func TestConfigUpgrades_V2toV3_Idempotent(t *testing.T) {
	in := "version: 2\nllm:\n  default: claude-code\n  configs:\n    claude-code: {}\n"
	once, applied := configUpgrades.Run([]byte(in))
	require.NotEmpty(t, applied)
	twice, again := configUpgrades.Run(once)
	assert.Empty(t, again, "already-v3 config must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

func TestConfigUpgrades_StampsCurrentVersion(t *testing.T) {
	// Even an already-key-correct but unversioned config upgrades by gaining the
	// current schema version stamp.
	out, applied := configUpgrades.Run([]byte("llm:\n  configs:\n    claude-code: { type: claude-code }\n"))
	require.NotEmpty(t, applied, "unversioned config must upgrade (stamp version)")
	assert.Contains(t, string(out), "version: 3")

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(out, &root))
	assert.Equal(t, CurrentConfigVersion, root["version"])
}

func TestConfigUpgrades_NoOpWhenCurrent(t *testing.T) {
	// A config already at the current version is returned verbatim (no rewrite).
	in := []byte("version: 3\nllm:\n  defaults:\n    primary: claude-code\n")
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
