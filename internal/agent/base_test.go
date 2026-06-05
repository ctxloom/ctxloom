// Backend base tests verify the shared functionality across all LM backends
// (claude-code, gemini, aider). The base backend provides common operations
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
	assert.NotNil(t, backend.Env)
	assert.Empty(t, backend.Args)
	assert.Empty(t, backend.Env)
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
// Environment merging combines backend defaults with per-request overrides,
// enabling context injection and backend-specific configuration.

func TestBaseBackend_BuildEnv(t *testing.T) {
	backend := NewBaseBackend("backend", "1.0")
	backend.Env = map[string]string{
		"BACKEND_VAR": "backend_value",
	}

	reqEnv := map[string]string{
		"REQUEST_VAR": "request_value",
	}

	env := backend.BuildEnv(reqEnv)

	// Backend env vars persist across all requests
	found := false
	for _, e := range env {
		if e == "BACKEND_VAR=backend_value" {
			found = true
			break
		}
	}
	assert.True(t, found, "Backend env var should be included")

	// Request env vars customize individual invocations
	found = false
	for _, e := range env {
		if e == "REQUEST_VAR=request_value" {
			found = true
			break
		}
	}
	assert.True(t, found, "Request env var should be included")
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
