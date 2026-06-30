package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
)

// resolveRunLLM precedence: explicit --llm override wins, then the profile's
// declared llm, then the primary role. A bad profile.llm is fault-tolerant
// (warn + fall back), while a bad --llm is a hard error.
func TestResolveRunLLM(t *testing.T) {
	cfg := &config.Config{LM: config.LMConfig{
		Configs: map[string]config.LLMConfig{
			"claude-fast": {Type: "claude-code"},
			"agy-code":    {Type: "antigravity"},
		},
		Defaults: config.RoleDefaults{Primary: "claude-fast"},
	}}

	t.Run("override wins over profile and primary", func(t *testing.T) {
		got, err := resolveRunLLM(cfg, "agy-code", "claude-fast")
		assert.NoError(t, err)
		assert.Equal(t, "agy-code", got)
	})

	t.Run("invalid override is a hard error", func(t *testing.T) {
		_, err := resolveRunLLM(cfg, "no-such-label", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no-such-label")
	})

	t.Run("profile llm used when no override", func(t *testing.T) {
		got, err := resolveRunLLM(cfg, "", "agy-code")
		assert.NoError(t, err)
		assert.Equal(t, "agy-code", got)
	})

	t.Run("bad profile llm falls back to primary, no error", func(t *testing.T) {
		got, err := resolveRunLLM(cfg, "", "no-such-label")
		assert.NoError(t, err)
		assert.Equal(t, "claude-fast", got)
	})

	t.Run("no override and no profile llm uses primary", func(t *testing.T) {
		got, err := resolveRunLLM(cfg, "", "")
		assert.NoError(t, err)
		assert.Equal(t, "claude-fast", got)
	})
}
