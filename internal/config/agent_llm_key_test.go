package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The config-key agent source is the one that would lose the value in SILENCE.
//
// A file-sourced agent meets KnownFields(true) and is rejected outright, but
// this decode path is lenient: an untouched `engine:` under `agents:` would be
// dropped without a word, and the binding would fall back to the profiles'
// llm — a different model, chosen by nobody, reported as success. That is the
// exact shape this codebase treats as its characteristic bug, which is why the
// refusal is asserted HERE and not only in the agents package.
func TestParseConfig_RefusesTheRetiredAgentEngineKey(t *testing.T) {
	_, err := ParseConfig([]byte(
		"version: 5\n" +
			"agents:\n" +
			"  coder:\n" +
			"    profiles: [dev]\n" +
			"    engine: claude-code\n"))

	require.Error(t, err, "a config-key agent using the retired `engine:` key must be refused, not silently read with the label dropped")
	assert.Contains(t, err.Error(), "llm",
		"the refusal must name the current spelling")
	assert.Contains(t, err.Error(), "coder",
		"and the agent it is complaining about, so a multi-agent config is actionable")
}

func TestParseConfig_ReadsTheAgentLLMLabel(t *testing.T) {
	cfg, err := ParseConfig([]byte(
		"version: 5\n" +
			"agents:\n" +
			"  coder:\n" +
			"    profiles: [dev]\n" +
			"    llm: claude-fast\n"))
	require.NoError(t, err)

	agent, ok := cfg.Agent("coder")
	require.True(t, ok, "the agent must be read")
	assert.Equal(t, "claude-fast", agent.LLM)
}

// The refusal fires on the KEY under an agent, not on the characters. A
// profile name, a model string or a prose value may contain the word without
// any such key existing.
func TestParseConfig_AllowsEngineAsAValueElsewhere(t *testing.T) {
	cfg, err := ParseConfig([]byte(
		"version: 5\n" +
			"agents:\n" +
			"  coder:\n" +
			"    profiles: [engine-tuning]\n" +
			"    llm: claude-fast\n"))
	require.NoError(t, err, "a mere mention must not be read as the retired key")

	agent, _ := cfg.Agent("coder")
	assert.Equal(t, []string{"engine-tuning"}, agent.Profiles)
	assert.False(t, strings.Contains(agent.LLM, "engine"))
}
