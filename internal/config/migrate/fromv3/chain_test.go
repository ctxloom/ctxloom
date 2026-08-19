// Package fromv3_test proves the migration OFF config version 3 end to end, through
// the exported load path a user actually takes.
//
// It lives in this directory rather than in config's own test files so that
// retiring support for v3 deletes the migration and its proof together — the
// directory is the unit of support.
package fromv3_test

import (
	"strings"
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

// lossyWarnings returns the dropped-setting diagnostics the load recorded, as
// plain strings.
//
// It reads them off the LOADED CONFIG rather than draining a package-global
// buffer, which is what the pre-move version of these tests had to do. That is
// not merely a mechanical change: a global buffer is shared with every other
// test in the binary, which is why those tests opened by draining it to discard
// "any earlier test's residue". Reading the warnings attached to the config
// under test removes the residue problem instead of working around it, and
// matches what the strict startup gate actually inspects.
func lossyWarnings(t *testing.T, in string) []string {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	var out []string
	for _, w := range cfg.GetWarnings() {
		if w.Kind == config.WarnKindMigrationLossy {
			out = append(out, w.Text)
		}
	}
	return out
}

// llmMap is a typed accessor for root["llm"] in a parsed upgraded config.
func llmMap(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	llm, ok := root["llm"].(map[string]any)
	require.True(t, ok, "llm should be a mapping")
	return llm
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

	root, applied := runConfigUpgrades(t, in)
	require.NotEmpty(t, applied)
	assert.Equal(t, 6, root["version"])

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

// A legacy v2 config keyed by backend chains through v2→v3 (type stamped from
// the key) into v3→v4 (gemini type flipped to antigravity, label preserved).
func TestConfigUpgrades_V3toV4_ChainsFromV2BackendKey(t *testing.T) {
	in := "version: 2\nllm:\n  default: gemini\n  configs:\n    gemini:\n      model: gemini-2.5-flash\n      approval_mode: auto\n"
	root, applied := runConfigUpgrades(t, in)
	require.NotEmpty(t, applied)
	assert.Equal(t, 6, root["version"])

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

// When an antigravity plugins block already exists, the dead gemini block is
// dropped rather than clobbering the user's antigravity hooks.
func TestConfigUpgrades_V3toV4_DoesNotClobberExistingAntigravityPlugins(t *testing.T) {
	in := "version: 3\nhooks:\n  plugins:\n    antigravity:\n      PreToolUse: [keep]\n    gemini:\n      PreToolUse: [old]\n"
	root, applied := runConfigUpgrades(t, in)
	require.NotEmpty(t, applied)

	hooks := root["hooks"].(map[string]any)["plugins"].(map[string]any)
	assert.NotContains(t, hooks, "gemini")
	pre := hooks["antigravity"].(map[string]any)["PreToolUse"].([]any)
	assert.Equal(t, []any{"keep"}, pre)
}

func TestConfigUpgrades_V3toV4_PreservesComments(t *testing.T) {
	in := "# header\nversion: 3\nllm:\n  configs:\n    gem:\n      # my gemini entry\n      type: gemini\n      model: gemini-2.5-flash\n      trust_workspace: true\n"
	out, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "# header", "top-level comment survives")
	assert.Contains(t, string(out), "# my gemini entry", "comment on the retyped entry survives")
	assert.Contains(t, string(out), "type: antigravity")
	assert.NotContains(t, string(out), "trust_workspace")
	assert.Contains(t, string(out), "version: 6")
}

func TestConfigUpgrades_V3toV4_Idempotent(t *testing.T) {
	in := "version: 3\nllm:\n  configs:\n    gem:\n      type: gemini\n      approval_mode: auto\n"
	once, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	twice, again := upgradedBytes(t, string(once))
	assert.Empty(t, again, "already-v4 config must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

// A v3 config without any gemini reference only gains the version stamp —
// nothing else in the document changes.

// A v3 config without any gemini reference only gains the version stamp —
// nothing else in the document changes.
func TestConfigUpgrades_V3toV4_CleanConfigOnlyGainsVersionStamp(t *testing.T) {
	// Block style throughout: re-encoding normalizes flow-map spacing, which
	// would fail the byte-for-byte comparison without being a real change.
	in := "version: 3\nllm:\n  configs:\n    claude-code:\n      type: claude-code\n  defaults:\n    primary: claude-code\nhooks:\n  plugins:\n    claude-code:\n      PreToolUse: []\n"
	out, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied, "stamping the version is itself a valid upgrade")
	assert.Equal(t, strings.Replace(in, "version: 3", "version: 6", 1), string(out),
		"a gemini-free config changes only its version stamp")
}

// TestConfigUpgrades_V4toV5_ProfilePromptSelectors pins the inline-profile prompt
// selector migration: an inline profile cherry-picking a bundle prompt via the
// legacy "#prompts/" selector is migrated to the commands section and stamped v5.

// TestConfigUpgrades_V3toV4_WarnsOnDroppedGeminiKnobs pins U049-F18: the
// gemini→antigravity migration deletes the user-set trust_workspace,
// approval_mode and binary_path keys, which have no antigravity equivalent.
// That deletion is an irreversible on-disk loss and must emit a lossy-migration
// warning naming each dropped key AND its value, the way migrateLLMv3 already
// does for its own lossy branch — not vanish silently.
func TestConfigUpgrades_V3toV4_WarnsOnDroppedGeminiKnobs(t *testing.T) {

	in := "version: 3\n" +
		"llm:\n" +
		"  configs:\n" +
		"    gem:\n" +
		"      type: gemini\n" +
		"      binary_path: /usr/local/bin/gemini\n" +
		"      trust_workspace: true\n" +
		"      approval_mode: yolo\n"

	root, applied := runConfigUpgrades(t, in)
	require.NotEmpty(t, applied)
	assert.Equal(t, 6, root["version"])

	warnings := lossyWarnings(t, in)
	joined := strings.Join(warnings, "\n")

	// Each dropped key must be named alongside its value and the config label.
	assert.Contains(t, joined, "trust_workspace", "the dropped trust_workspace key must be named")
	assert.Contains(t, joined, "true", "trust_workspace's value must be named")
	assert.Contains(t, joined, "approval_mode", "the dropped approval_mode key must be named")
	assert.Contains(t, joined, "yolo", "approval_mode's value must be named")
	assert.Contains(t, joined, "binary_path", "the dropped binary_path key must be named")
	assert.Contains(t, joined, "/usr/local/bin/gemini", "binary_path's value must be named")
	assert.Contains(t, joined, "gem", "the affected llm config label must be named so the user can act")
}

// TestConfigUpgrades_V3toV4_NoWarningWhenKnobsAbsent is the control: a gemini
// config that never set those knobs loses nothing, so it must warn about
// nothing.

// TestConfigUpgrades_V3toV4_NoWarningWhenKnobsAbsent is the control: a gemini
// config that never set those knobs loses nothing, so it must warn about
// nothing.
func TestConfigUpgrades_V3toV4_NoWarningWhenKnobsAbsent(t *testing.T) {

	in := "version: 3\nllm:\n  configs:\n    gem:\n      type: gemini\n      model: gemini-2.5-flash\n"
	_, applied := runConfigUpgrades(t, in)
	require.NotEmpty(t, applied)

	assert.Empty(t, lossyWarnings(t, in), "a gemini config with no dropped knobs must warn about nothing")
}
