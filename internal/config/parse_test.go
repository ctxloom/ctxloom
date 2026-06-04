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

	require.Len(t, cfg.LM.Configs, 2)
	primary := cfg.LM.Configs["claude-code"]
	assert.Equal(t, "claude-code", primary.Type)
	assert.Equal(t, "primary", primary.Role)
	assert.Equal(t, "opus", primary.Body["model"])
	// role binds to its named field, never the inline body.
	assert.NotContains(t, primary.Body, "role")

	assert.Equal(t, "claude-code", cfg.LM.Defaults.Primary)
	assert.Equal(t, "claude-fast", cfg.LM.Defaults.Fast)
}

// TestMarshal_StripsRole confirms the persist path drops the registry-only role
// from every entry while leaving the in-memory config's roles untouched.
func TestMarshal_StripsRole(t *testing.T) {
	cfg := &Config{
		LM: LMConfig{
			Configs: map[string]LLMConfig{
				"claude-code": {Type: "claude-code", Role: "primary", Body: map[string]interface{}{"model": "opus"}},
			},
			Defaults: RoleDefaults{Primary: "claude-code", Fast: "claude-code"},
		},
	}

	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "role:")

	// The in-memory registry keeps its role — Marshal must not mutate it.
	assert.Equal(t, "primary", cfg.LM.Configs["claude-code"].Role)

	// The model and type survive the round trip.
	back, err := ParseConfig(data)
	require.NoError(t, err)
	entry := back.LM.Configs["claude-code"]
	assert.Equal(t, "claude-code", entry.Type)
	assert.Equal(t, "opus", entry.Body["model"])
	assert.Empty(t, entry.Role)
}
