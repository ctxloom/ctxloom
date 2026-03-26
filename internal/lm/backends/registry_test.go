// Backend registry tests verify that all supported LM backends are registered
// and accessible. The registry enables SCM to work with multiple AI coding
// assistants (Claude Code, Gemini CLI, Codex) through a unified interface.
package backends

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Backend Registration Tests
// =============================================================================
// All built-in backends must be registered and retrievable by name.

func TestRegistry_GetBuiltinBackends(t *testing.T) {
	// Every supported backend must be registered for `scm run` to work
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
