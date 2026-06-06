// Backend registry tests verify that all supported LM backends are registered
// and accessible. The registry enables ctxloom to work with multiple AI coding
// assistants (Claude Code, Gemini CLI, Codex) through a unified interface.
package backends

import (
	"sort"
	"testing"

	"github.com/ctxloom/claude"
	"github.com/ctxloom/gemini"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Backend Registration Tests
// =============================================================================
// All built-in backends must be registered and retrievable by name.

func TestRegistry_GetBuiltinBackends(t *testing.T) {
	// Every supported backend must be registered for `ctxloom run` to work
	builtinNames := []string{
		"claude-code",
		"gemini",
		"codex",
		"mock",
	}

	for _, name := range builtinNames {
		t.Run(name, func(t *testing.T) {
			backend := Get(name)
			assert.NotNil(t, backend)
			assert.Equal(t, name, backend.Name())
		})
	}
}

func TestRegistry_GetNonExistent(t *testing.T) {
	// Unknown backends return nil - enables graceful error handling
	backend := Get("nonexistent-backend")
	assert.Nil(t, backend)
}

func TestRegistry_Exists(t *testing.T) {
	// Exists check enables validation before attempting to run
	assert.True(t, Exists("claude-code"))
	assert.True(t, Exists("mock"))
	assert.False(t, Exists("nonexistent"))
}

func TestRegistry_List(t *testing.T) {
	// List enables help output and tab completion
	names := List()
	assert.GreaterOrEqual(t, len(names), 4) // At least the builtin backends

	sort.Strings(names)
	assert.Contains(t, names, "claude-code")
	assert.Contains(t, names, "mock")
}

func TestGetDefaultBinary(t *testing.T) {
	t.Run("returns binary for registered backend", func(t *testing.T) {
		// Mock backend returns empty string since it has no real binary
		binary := GetDefaultBinary("mock")
		assert.Equal(t, "", binary)
	})

	t.Run("returns empty for non-existent backend", func(t *testing.T) {
		binary := GetDefaultBinary("nonexistent")
		assert.Equal(t, "", binary)
	})
}

func TestIsAvailable(t *testing.T) {
	t.Run("mock backend is not available (no real binary)", func(t *testing.T) {
		// Mock backend doesn't have a real binary path, so it won't be "available"
		available := IsAvailable("mock")
		assert.False(t, available)
	})

	t.Run("non-existent backend is not available", func(t *testing.T) {
		available := IsAvailable("nonexistent-backend")
		assert.False(t, available)
	})
}

// TestDecodeLLMConfig verifies the backend config registry decodes a raw body
// into the backend's own typed struct, keyed solely by the type discriminator.
func TestDecodeLLMConfig(t *testing.T) {
	t.Run("claude-code decodes its fields", func(t *testing.T) {
		bc, err := DecodeLLMConfig("claude-code", map[string]interface{}{
			"model":       "haiku",
			"binary_path": "/custom/claude",
		})
		require.NoError(t, err)
		cc, ok := bc.(*claude.ClaudeConfig)
		require.True(t, ok, "decoder must yield *ClaudeConfig")
		assert.Equal(t, "haiku", cc.Model)
		assert.Equal(t, "/custom/claude", cc.BinaryPath)
	})

	t.Run("gemini decodes trust_workspace", func(t *testing.T) {
		bc, err := DecodeLLMConfig("gemini", map[string]interface{}{
			"model":           "gemini-2.5-pro",
			"trust_workspace": true,
		})
		require.NoError(t, err)
		gc, ok := bc.(*gemini.GeminiConfig)
		require.True(t, ok)
		assert.Equal(t, "gemini-2.5-pro", gc.Model)
		require.NotNil(t, gc.TrustWorkspace)
		assert.True(t, *gc.TrustWorkspace)
	})

	t.Run("unknown type errors", func(t *testing.T) {
		_, err := DecodeLLMConfig("nope", nil)
		assert.Error(t, err)
	})
}

// TestConfiguredBackend builds a backend from a typed config and applies it.
func TestConfiguredBackend(t *testing.T) {
	b := ConfiguredBackend(&claude.ClaudeConfig{BinaryPath: "/custom/claude"})
	require.NotNil(t, b)
	bp, ok := b.(BinaryPathProvider)
	require.True(t, ok)
	assert.Equal(t, "/custom/claude", bp.GetBinaryPath())
}
