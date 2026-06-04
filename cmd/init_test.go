package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestGenerateConfig covers the ctxloom init config builder. The selected
// engine's backend type lands in the registry, and the output must be valid
// YAML ending in a newline.
func TestGenerateConfig(t *testing.T) {
	for _, engine := range []string{"claude-code", "gemini", "codex"} {
		t.Run(engine, func(t *testing.T) {
			data, err := operations.BuildInitialConfig(engine)
			require.NoError(t, err)
			body := string(data)
			assert.Contains(t, body, "type: "+engine,
				"engine must appear as a registry entry type")
			assert.NotContains(t, body, "role:",
				"role is registry-only and stripped on write")
			assert.True(t, strings.HasSuffix(body, "\n"),
				"config must end with newline (POSIX-friendly + diff-friendly)")
		})
	}
}

func TestGenerateConfig_DefaultsBlock(t *testing.T) {
	data, err := operations.BuildInitialConfig("claude-code")
	require.NoError(t, err)
	body := string(data)
	// The scaffold settings survive into the written config.
	assert.Contains(t, body, "use_distilled: true")
	assert.Contains(t, body, "auto_register_ctxloom: true")
	// The engine's role pair is wired into llm.defaults.
	assert.Contains(t, body, "primary: claude-code")
	assert.Contains(t, body, "fast: claude-fast")
}
