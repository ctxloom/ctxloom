package cmd

import (
	"testing"

	"github.com/ctxloom/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoLabelConfig builds a config with two labels of the same backend type —
// the shape where a map-ordered type scan would pick a random entry per
// process.
func twoLabelConfig() *config.Config {
	return &config.Config{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"alpha": {Type: "claude-code", Body: map[string]interface{}{"model": "model-alpha"}},
				"beta":  {Type: "claude-code", Body: map[string]interface{}{"model": "model-beta"}},
			},
		},
	}
}

func decodedClaudeModel(t *testing.T, cfg *config.Config) string {
	t.Helper()
	bc := decodeBackendConfigForType(cfg, "claude-code")
	cc, ok := bc.(*claude.ClaudeConfig)
	require.True(t, ok, "expected *claude.ClaudeConfig, got %#v", bc)
	return cc.Model
}

func TestDecodeBackendConfigForType_PrefersPrimaryLabel(t *testing.T) {
	cfg := twoLabelConfig()
	cfg.LM.Defaults.Primary = "beta"

	assert.Equal(t, "model-beta", decodedClaudeModel(t, cfg))
}

func TestDecodeBackendConfigForType_DeterministicWithoutPrimary(t *testing.T) {
	cfg := twoLabelConfig()

	assert.Equal(t, "model-alpha", decodedClaudeModel(t, cfg),
		"ties resolve to the lexicographically first label")
}

func TestDecodeBackendConfigForType_PrimaryOfOtherTypeFallsBack(t *testing.T) {
	cfg := twoLabelConfig()
	cfg.LM.Configs["agy"] = config.LLMConfig{Type: "antigravity", Body: map[string]interface{}{}}
	cfg.LM.Defaults.Primary = "agy"

	assert.Equal(t, "model-alpha", decodedClaudeModel(t, cfg),
		"a primary of a different type falls back to the sorted-label scan")
}
