// Package fromv2_test proves the migration OFF config version 2 end to end, through
// the exported load path a user actually takes.
//
// It lives in this directory rather than in config's own test files so that
// retiring support for v2 deletes the migration and its proof together — the
// directory is the unit of support.
package fromv2_test

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

	root, applied := runConfigUpgrades(t, in)
	require.NotEmpty(t, applied)

	assert.Equal(t, 6, root["version"], "full pipeline lands on the current version")

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

	// profiles.definitions survives; v5→v6 retired profiles.defaults, moving it
	// into the synthesized default agent (asserted below).
	prof := root["profiles"].(map[string]any)
	assert.NotContains(t, prof, "defaults", "v5→v6 retires profiles.defaults")
	defs := prof["definitions"].(map[string]any)
	assert.Contains(t, defs, "my-profile")

	// v5→v6: profiles.defaults → agents.default + default_agent, engine ← primary.
	assert.Equal(t, "default", root["default_agent"])
	agent := root["agents"].(map[string]any)["default"].(map[string]any)
	assert.Contains(t, agent["profiles"], "proj/dev")
	assert.Equal(t, "claude-code", agent["llm"], "default agent llm ← llm.defaults.primary")
	assert.NotContains(t, agent, "runtime", "runtime is deliberately left unset -- empty already means \"host\" (Agent.Runtime's own doc), and writing it explicitly would trip layerscope's project-file restriction on a migrated config")

	// top-level defaults bag is gone
	assert.NotContains(t, root, "defaults")
}

// Compaction with no explicit llm points the fast role at the primary label.

// Compaction with no explicit llm points the fast role at the primary label.
func TestConfigUpgrades_V2toV3_CompactionFallsBackToPrimary(t *testing.T) {
	in := "version: 2\nllm:\n  default: claude-code\n  configs:\n    claude-code: {}\n  compaction:\n    chunks: 8000\n"
	root, _ := runConfigUpgrades(t, in)
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
	out, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "# header", "top-level comment survives")
	assert.Contains(t, string(out), "# keep me", "relocated use_distilled keeps its comment")
	assert.Contains(t, string(out), "version: 6")
}

func TestConfigUpgrades_V2toV3_Idempotent(t *testing.T) {
	in := "version: 2\nllm:\n  default: claude-code\n  configs:\n    claude-code: {}\n"
	once, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	twice, again := upgradedBytes(t, string(once))
	assert.Empty(t, again, "already-v3 config must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

// TestConfigUpgrades_V3toV4 covers the gemini→antigravity backend replacement:
// typed entries flip their discriminator and shed the gemini-only knobs, and
// the per-backend plugins blocks follow the rename.
