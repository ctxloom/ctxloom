package config

import (
	"strings"
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

	assert.Equal(t, 4, root["version"], "full pipeline lands on the current version")

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
	assert.Contains(t, string(out), "version: 4")
}

func TestConfigUpgrades_V2toV3_Idempotent(t *testing.T) {
	in := "version: 2\nllm:\n  default: claude-code\n  configs:\n    claude-code: {}\n"
	once, applied := configUpgrades.Run([]byte(in))
	require.NotEmpty(t, applied)
	twice, again := configUpgrades.Run(once)
	assert.Empty(t, again, "already-v3 config must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

// TestConfigUpgrades_V3toV4 covers the gemini→antigravity backend replacement:
// typed entries flip their discriminator and shed the gemini-only knobs, and
// the per-backend plugins blocks follow the rename.
func TestConfigUpgrades_V3toV4_GeminiToAntigravity(t *testing.T) {
	in := "version: 3\n" +
		"llm:\n" +
		"  configs:\n" +
		"    gem:\n" +
		"      type: gemini\n" +
		"      model: gemini-2.5-flash\n" +
		"      binary_path: /usr/local/bin/gemini\n" +
		"      trust_workspace: true\n" +
		"      approval_mode: yolo\n" +
		"      args: [--sandbox]\n" +
		"      env:\n" +
		"        GEMINI_API_KEY: secret\n" +
		"    claude-code: { type: claude-code }\n" +
		"  defaults:\n" +
		"    primary: gem\n" +
		"hooks:\n" +
		"  plugins:\n" +
		"    gemini:\n" +
		"      PreToolUse: []\n" +
		"mcp:\n" +
		"  plugins:\n" +
		"    gemini:\n" +
		"      srv:\n" +
		"        command: x\n"

	root, applied := runConfigUpgrades(in)
	require.NotEmpty(t, applied)
	assert.Equal(t, 4, root["version"])

	llm := llmMap(t, root)
	configs := llm["configs"].(map[string]any)
	gem := configs["gem"].(map[string]any)
	assert.Equal(t, "antigravity", gem["type"])
	// gemini-only knobs have no antigravity equivalent (now schema-invalid).
	assert.NotContains(t, gem, "trust_workspace")
	assert.NotContains(t, gem, "approval_mode")
	// binary_path pointed at the gemini binary; the default agy binary is correct.
	assert.NotContains(t, gem, "binary_path")
	// A stale-but-schema-valid model is the user's to update; args/env survive.
	assert.Equal(t, "gemini-2.5-flash", gem["model"])
	assert.Equal(t, []any{"--sandbox"}, gem["args"])
	assert.Equal(t, map[string]any{"GEMINI_API_KEY": "secret"}, gem["env"])

	// Sibling entries and labels (incl. role references) are untouched.
	assert.Equal(t, "claude-code", configs["claude-code"].(map[string]any)["type"])
	assert.Equal(t, "gem", llm["defaults"].(map[string]any)["primary"])

	// hooks.plugins.gemini / mcp.plugins.gemini follow the backend rename.
	hooks := root["hooks"].(map[string]any)["plugins"].(map[string]any)
	assert.NotContains(t, hooks, "gemini")
	require.Contains(t, hooks, "antigravity")
	mcp := root["mcp"].(map[string]any)["plugins"].(map[string]any)
	assert.NotContains(t, mcp, "gemini")
	require.Contains(t, mcp, "antigravity")
	srv := mcp["antigravity"].(map[string]any)["srv"].(map[string]any)
	assert.Equal(t, "x", srv["command"])
}

// A legacy v2 config keyed by backend chains through v2→v3 (type stamped from
// the key) into v3→v4 (gemini type flipped to antigravity, label preserved).
func TestConfigUpgrades_V3toV4_ChainsFromV2BackendKey(t *testing.T) {
	in := "version: 2\nllm:\n  default: gemini\n  configs:\n    gemini:\n      model: gemini-2.5-flash\n      approval_mode: auto\n"
	root, applied := runConfigUpgrades(in)
	require.NotEmpty(t, applied)
	assert.Equal(t, 4, root["version"])

	llm := llmMap(t, root)
	// The label stays "gemini" — it is just a label; only the type changes.
	gem := llm["configs"].(map[string]any)["gemini"].(map[string]any)
	assert.Equal(t, "antigravity", gem["type"])
	assert.NotContains(t, gem, "approval_mode")
	assert.Equal(t, "gemini-2.5-flash", gem["model"])
	assert.Equal(t, "gemini", llm["defaults"].(map[string]any)["primary"])
}

// When an antigravity plugins block already exists, the dead gemini block is
// dropped rather than clobbering the user's antigravity hooks.
func TestConfigUpgrades_V3toV4_DoesNotClobberExistingAntigravityPlugins(t *testing.T) {
	in := "version: 3\nhooks:\n  plugins:\n    antigravity:\n      PreToolUse: [keep]\n    gemini:\n      PreToolUse: [old]\n"
	root, applied := runConfigUpgrades(in)
	require.NotEmpty(t, applied)

	hooks := root["hooks"].(map[string]any)["plugins"].(map[string]any)
	assert.NotContains(t, hooks, "gemini")
	pre := hooks["antigravity"].(map[string]any)["PreToolUse"].([]any)
	assert.Equal(t, []any{"keep"}, pre)
}

func TestConfigUpgrades_V3toV4_PreservesComments(t *testing.T) {
	in := "# header\nversion: 3\nllm:\n  configs:\n    gem:\n      # my gemini entry\n      type: gemini\n      model: gemini-2.5-flash\n      trust_workspace: true\n"
	out, applied := configUpgrades.Run([]byte(in))
	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "# header", "top-level comment survives")
	assert.Contains(t, string(out), "# my gemini entry", "comment on the retyped entry survives")
	assert.Contains(t, string(out), "type: antigravity")
	assert.NotContains(t, string(out), "trust_workspace")
	assert.Contains(t, string(out), "version: 4")
}

func TestConfigUpgrades_V3toV4_Idempotent(t *testing.T) {
	in := "version: 3\nllm:\n  configs:\n    gem:\n      type: gemini\n      approval_mode: auto\n"
	once, applied := configUpgrades.Run([]byte(in))
	require.NotEmpty(t, applied)
	twice, again := configUpgrades.Run(once)
	assert.Empty(t, again, "already-v4 config must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

// A v3 config without any gemini reference only gains the version stamp —
// nothing else in the document changes.
func TestConfigUpgrades_V3toV4_CleanConfigOnlyGainsVersionStamp(t *testing.T) {
	// Block style throughout: re-encoding normalizes flow-map spacing, which
	// would fail the byte-for-byte comparison without being a real change.
	in := "version: 3\nllm:\n  configs:\n    claude-code:\n      type: claude-code\n  defaults:\n    primary: claude-code\nhooks:\n  plugins:\n    claude-code:\n      PreToolUse: []\n"
	out, applied := configUpgrades.Run([]byte(in))
	require.NotEmpty(t, applied, "stamping the version is itself a valid upgrade")
	assert.Equal(t, strings.Replace(in, "version: 3", "version: 4", 1), string(out),
		"a gemini-free config changes only its version stamp")
}

func TestConfigUpgrades_StampsCurrentVersion(t *testing.T) {
	// Even an already-key-correct but unversioned config upgrades by gaining the
	// current schema version stamp.
	out, applied := configUpgrades.Run([]byte("llm:\n  configs:\n    claude-code: { type: claude-code }\n"))
	require.NotEmpty(t, applied, "unversioned config must upgrade (stamp version)")
	assert.Contains(t, string(out), "version: 4")

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(out, &root))
	assert.Equal(t, CurrentConfigVersion, root["version"])
}

func TestConfigUpgrades_NoOpWhenCurrent(t *testing.T) {
	// A config already at the current version is returned verbatim (no rewrite).
	in := []byte("version: 4\nllm:\n  defaults:\n    primary: claude-code\n")
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
