// Backend base tests verify the shared functionality across all LM backends.
// The base backend provides common operations
// like environment variable merging and working directory management.
package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Backend Construction Tests
// =============================================================================
// Backends must initialize with sensible defaults for optional fields.

func TestNewBaseBackend(t *testing.T) {
	backend := NewBaseBackend("test-backend", "1.0.0")

	assert.Equal(t, "test-backend", backend.name)
	assert.Equal(t, "1.0.0", backend.version)
	assert.NotNil(t, backend.Args)
	assert.Empty(t, backend.Args)
}

func TestBaseBackend_Name(t *testing.T) {
	backend := NewBaseBackend("my-backend", "2.0")
	assert.Equal(t, "my-backend", backend.Name())
}

func TestBaseBackend_Version(t *testing.T) {
	backend := NewBaseBackend("backend", "3.1.4")
	assert.Equal(t, "3.1.4", backend.Version())
}

func TestBaseBackend_SupportedModes(t *testing.T) {
	// All backends support interactive (REPL) and oneshot (single command) modes
	backend := NewBaseBackend("backend", "1.0")
	modes := backend.SupportedModes()

	assert.Len(t, modes, 2)
	assert.Contains(t, modes, ModeInteractive)
	assert.Contains(t, modes, ModeOneshot)
}

// =============================================================================
// Working Directory Tests
// =============================================================================
// Work directory determines where the backend process runs from.

func TestBaseBackend_WorkDir(t *testing.T) {
	backend := NewBaseBackend("backend", "1.0")

	// Default is current directory - safe for most operations
	assert.Equal(t, ".", backend.WorkDir())

	// Custom paths enable project-relative execution
	backend.SetWorkDir("/custom/path")
	assert.Equal(t, "/custom/path", backend.WorkDir())
}

// =============================================================================
// Environment Building Tests
// =============================================================================
// The launched process sees the ambient environment plus the request's own
// entries: that is how a request's context injection reaches the engine, and
// how the engine's own credentials do (ctxloom carries none).

func TestBaseBackend_BuildEnv(t *testing.T) {
	backend := NewBaseBackend("backend", "1.0")
	t.Setenv("AMBIENT_VAR", "ambient_value")

	env := backend.BuildEnv(map[string]string{"REQUEST_VAR": "request_value"})

	assert.Contains(t, env, "AMBIENT_VAR=ambient_value", "the ambient environment must reach the process")
	assert.Contains(t, env, "REQUEST_VAR=request_value", "the request's entries must reach the process")
}

// =============================================================================
// Context Assembly Tests
// =============================================================================
// Context assembly combines multiple fragments into a single document for AI
// consumption. Proper assembly ensures fragments are separated and readable.

func TestAssembleContext(t *testing.T) {
	t.Run("empty fragments", func(t *testing.T) {
		// No fragments should produce empty output - not inject blank context
		result := AssembleContext(nil)
		assert.Empty(t, result)

		result = AssembleContext([]*Fragment{})
		assert.Empty(t, result)
	})

	t.Run("single fragment", func(t *testing.T) {
		// Single fragment needs no separator
		frags := []*Fragment{
			{Content: "Hello world"},
		}
		result := AssembleContext(frags)
		assert.Equal(t, "Hello world", result)
	})

	t.Run("multiple fragments", func(t *testing.T) {
		// Multiple fragments are separated for readability
		frags := []*Fragment{
			{Content: "First fragment"},
			{Content: "Second fragment"},
			{Content: "Third fragment"},
		}
		result := AssembleContext(frags)
		assert.Contains(t, result, "First fragment")
		assert.Contains(t, result, "Second fragment")
		assert.Contains(t, result, "Third fragment")
		assert.Contains(t, result, "---") // Separator
	})

	t.Run("skips empty content", func(t *testing.T) {
		// Empty fragments are noise - skip them entirely
		frags := []*Fragment{
			{Content: "First"},
			{Content: ""},
			{Content: "Third"},
		}
		result := AssembleContext(frags)
		assert.Contains(t, result, "First")
		assert.Contains(t, result, "Third")
		// Only one separator since empty was skipped
		assert.Equal(t, 1, countSubstring(result, "---"))
	})

	t.Run("trims whitespace", func(t *testing.T) {
		// Normalize whitespace for consistent output
		frags := []*Fragment{
			{Content: "  Content with spaces  "},
		}
		result := AssembleContext(frags)
		assert.Equal(t, "Content with spaces", result)
	})
}

// =============================================================================
// Prompt Content Tests
// =============================================================================
// Prompt extraction handles nil/empty cases safely for optional prompts.

func TestGetPromptContent(t *testing.T) {
	t.Run("nil prompt", func(t *testing.T) {
		// Nil prompt is valid - user may not specify one
		result := GetPromptContent(nil)
		assert.Empty(t, result)
	})

	t.Run("with content", func(t *testing.T) {
		prompt := &Fragment{Content: "Prompt content"}
		result := GetPromptContent(prompt)
		assert.Equal(t, "Prompt content", result)
	})

	t.Run("empty content", func(t *testing.T) {
		prompt := &Fragment{Content: ""}
		result := GetPromptContent(prompt)
		assert.Empty(t, result)
	})
}

func countSubstring(s, substr string) int {
	count := 0
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}
