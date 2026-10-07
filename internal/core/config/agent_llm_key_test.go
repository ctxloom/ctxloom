package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_ReadsTheAgentLLMLabel(t *testing.T) {
	cfg, err := ParseConfig([]byte(
		"schema_version: 7\n" +
			"agents:\n" +
			"  coder:\n" +
			"    profiles: [dev]\n" +
			"    llm: claude-fast\n"))
	require.NoError(t, err)

	agent, ok := cfg.Agent("coder")
	require.True(t, ok, "the agent must be read")
	assert.Equal(t, "claude-fast", agent.LLM)
}
