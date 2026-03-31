// Backend registry tests verify that all supported LM backends are registered
// and accessible. The registry enables SCM to work with multiple AI coding
// assistants (Claude Code, Gemini CLI, Codex) through a unified interface.
package backends

import (
	"sort"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/stretchr/testify/assert"
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

func TestApplyPluginConfig(t *testing.T) {
	t.Run("with nil config", func(t *testing.T) {
		backend := Get("mock")
		// Should not panic with nil config
		ApplyPluginConfig(backend, nil)
		assert.NotNil(t, backend)
	})

	t.Run("with configurable backend", func(t *testing.T) {
		backend := Get("mock")
		// Mock might be configurable, just verify it doesn't panic
		config := &config.PluginConfig{}
		ApplyPluginConfig(backend, config)
		assert.NotNil(t, backend)
	})
}

func TestContextFileName(t *testing.T) {
	t.Run("returns context file name for registered backend", func(t *testing.T) {
		fileName := ContextFileName("claude-code")
		// Claude Code uses CLAUDE.md
		assert.Equal(t, "CLAUDE.md", fileName)
	})

	t.Run("returns empty for non-existent backend", func(t *testing.T) {
		fileName := ContextFileName("nonexistent")
		assert.Equal(t, "", fileName)
	})

	t.Run("returns gemini context file name", func(t *testing.T) {
		fileName := ContextFileName("gemini")
		assert.Equal(t, "GEMINI.md", fileName)
	})
}

func TestContextFileNames(t *testing.T) {
	fileNames := ContextFileNames()

	// Should include backends with non-empty context files
	assert.Contains(t, fileNames, "claude-code")
	assert.Contains(t, fileNames, "gemini")
	assert.Equal(t, "CLAUDE.md", fileNames["claude-code"])
	assert.Equal(t, "GEMINI.md", fileNames["gemini"])

	// Mock has empty context file name, should not be included
	_, hasMock := fileNames["mock"]
	assert.False(t, hasMock)
}
