package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseConfig_RoundTrips confirms ParseConfig reads a registry verbatim
// (no default overlay) and that the role marker binds to its own field rather
// than leaking into the inline body.
func TestParseConfig_RoundTrips(t *testing.T) {
	src := []byte(`version: 3
llm:
  configs:
    claude-code:
      type: claude-code
      model: opus
      role: primary
    claude-fast:
      type: claude-code
      model: haiku
      role: fast
  defaults:
    primary: claude-code
    fast: claude-fast
`)
	cfg, err := ParseConfig(src)
	require.NoError(t, err)

	require.Len(t, cfg.lm.Configs, 2)
	primary := cfg.lm.Configs["claude-code"]
	assert.Equal(t, "claude-code", primary.Type)
	assert.Equal(t, "primary", primary.Role)
	assert.Equal(t, "opus", primary.Body["model"])
	// role binds to its named field, never the inline body.
	assert.NotContains(t, primary.Body, "role")

	assert.Equal(t, "claude-code", cfg.lm.Defaults.Primary)
	assert.Equal(t, "claude-fast", cfg.lm.Defaults.Fast)
}
