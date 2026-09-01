// Backend base tests verify the shared functionality across all LM backends
// (claude-code, codex). The base backend provides common operations
// like environment variable merging and working directory management.
package agent

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestRun_CarriesRunIdentityOntoTheLaunchSpec pins the three fields ruling (A)
// put on LaunchSpec. They exist so a launcher that HOSTS a process somewhere
// addressable can name the run and decide whether its pane may be injected
// into, and none of them is derivable from the exec fields beside them: a
// launcher handed only BinaryPath cannot tell claude's TUI from its ACP
// adapter, and the two answer a bracketed paste differently.
//
// Surface is asserted for BOTH launch modes because it is DERIVED from the
// interactive flag rather than passed in. A single-mode test would pass
// against a constant, which is the likeliest way to get this wrong.
func TestRun_CarriesRunIdentityOntoTheLaunchSpec(t *testing.T) {
	newBackend := func() (*BaseBackend, *LaunchSpec) {
		var captured LaunchSpec
		b := NewBaseBackend("claude-code", "1.0.0")
		b.BinaryPath = "/bin/true"
		b.SetLauncher(func(_ context.Context, spec LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan WindowSize) (int32, error) {
			captured = spec
			return 0, nil
		})
		return &b, &captured
	}
	env := map[string]string{SessionHarpEnv: "swift-amber-falcon"}

	t.Run("interactive", func(t *testing.T) {
		b, got := newBackend()
		_, err := b.RunInteractive(context.Background(), nil, env, nil, nil, io.Discard, io.Discard, nil)
		require.NoError(t, err)
		assert.Equal(t, "swift-amber-falcon", got.Harp, "the harp must come off the request env, which is where the caller stamps it")
		assert.Equal(t, "claude-code", got.Engine, "the engine is the backend's own name")
		assert.Equal(t, CLISurfaceInteractive, got.Surface)
	})

	t.Run("non-interactive", func(t *testing.T) {
		b, got := newBackend()
		_, err := b.RunNonInteractive(context.Background(), nil, env, nil, io.Discard, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "swift-amber-falcon", got.Harp)
		assert.Equal(t, "claude-code", got.Engine)
		assert.Equal(t, CLISurfaceOneshot, got.Surface, "a oneshot run must NOT be labelled interactive")
	})

	// A run nobody named a harp for must not inherit one: "" addresses no
	// pane, which is the honest answer. Defaulting to anything else would
	// point injection at a pane belonging to a different run.
	t.Run("no harp in env", func(t *testing.T) {
		b, got := newBackend()
		_, err := b.RunNonInteractive(context.Background(), nil, nil, nil, io.Discard, io.Discard)
		require.NoError(t, err)
		assert.Empty(t, got.Harp)
	})
}
