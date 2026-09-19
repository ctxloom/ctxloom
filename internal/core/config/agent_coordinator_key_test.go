package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The config-key agent source is the one that would lose `coordinator:` in
// SILENCE. A file-sourced agent meets KnownFields(true), but this decode path
// is lenient, so an untouched key under `agents:` is dropped without a word.
//
// That matters more here than for a rename. When the leaf tool-gate first
// shipped, every binding that had to delegate was marked `coordinator: true`
// or its children lost the delegation tools — so the key is written into real
// configs, this repo's own among them. Dropping it quietly leaves a binding
// authored to delegate unable to, reported as success.
func TestParseConfig_RefusesTheRemovedAgentCoordinatorKey(t *testing.T) {
	_, err := ParseConfig([]byte(
		"version: 5\n" +
			"agents:\n" +
			"  coord:\n" +
			"    profiles: [dev]\n" +
			"    llm: claude-fast\n" +
			"    coordinator: true\n"))

	require.Error(t, err, "a config-key agent using the removed `coordinator:` key must be refused, not silently read with the flag dropped")
	assert.Contains(t, err.Error(), "delegation.depth",
		"the refusal must name what replaced the flag, or the reader cannot tell whether their delegation still works")
	assert.Contains(t, err.Error(), "coord",
		"and the agent it is complaining about, so a multi-agent config is actionable")
}

// `coordinator: false` is refused too — it reads as "asking for the default",
// which is precisely why accepting it would mislead.
func TestParseConfig_RefusesTheRemovedCoordinatorKeyWhenFalse(t *testing.T) {
	_, err := ParseConfig([]byte(
		"version: 5\n" +
			"agents:\n" +
			"  leaf:\n" +
			"    profiles: [dev]\n" +
			"    llm: claude-fast\n" +
			"    coordinator: false\n"))
	require.Error(t, err, "`coordinator: false` must be refused as well: the key is gone, not defaulted")
}

// The refusal fires on the KEY under an agent, not on the characters appearing
// somewhere in the document. Without this, a profile named after the concept
// would be rejected and the gate would be worse than useless.
func TestParseConfig_AllowsCoordinatorAsAValueElsewhere(t *testing.T) {
	cfg, err := ParseConfig([]byte(
		"version: 5\n" +
			"agents:\n" +
			"  coord:\n" +
			"    profiles: [coordinator]\n" +
			"    llm: claude-fast\n"))
	require.NoError(t, err, "a mere mention must not be read as the removed key")

	agent, ok := cfg.Agent("coord")
	require.True(t, ok)
	assert.Equal(t, []string{"coordinator"}, agent.Profiles,
		"the coordinator PROFILE survives; only the per-binding privilege flag went")
}
