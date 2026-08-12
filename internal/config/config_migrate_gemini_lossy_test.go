package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigUpgrades_V3toV4_WarnsOnDroppedGeminiKnobs pins U049-F18: the
// gemini→antigravity migration deletes the user-set trust_workspace,
// approval_mode and binary_path keys, which have no antigravity equivalent.
// That deletion is an irreversible on-disk loss and must emit a lossy-migration
// warning naming each dropped key AND its value, the way migrateLLMv3 already
// does for its own lossy branch — not vanish silently.
func TestConfigUpgrades_V3toV4_WarnsOnDroppedGeminiKnobs(t *testing.T) {
	drainMigrationWarnings() // isolate from any earlier test's residue

	in := "version: 3\n" +
		"llm:\n" +
		"  configs:\n" +
		"    gem:\n" +
		"      type: gemini\n" +
		"      binary_path: /usr/local/bin/gemini\n" +
		"      trust_workspace: true\n" +
		"      approval_mode: yolo\n"

	root, applied := runConfigUpgrades(in)
	require.NotEmpty(t, applied)
	assert.Equal(t, 6, root["version"])

	warnings := drainMigrationWarnings()
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
func TestConfigUpgrades_V3toV4_NoWarningWhenKnobsAbsent(t *testing.T) {
	drainMigrationWarnings()

	in := "version: 3\nllm:\n  configs:\n    gem:\n      type: gemini\n      model: gemini-2.5-flash\n"
	_, applied := runConfigUpgrades(in)
	require.NotEmpty(t, applied)

	assert.Empty(t, drainMigrationWarnings(), "a gemini config with no dropped knobs must warn about nothing")
}
