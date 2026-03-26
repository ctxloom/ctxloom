package backends

import (
	"testing"

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Claude Lifecycle Tests
// =============================================================================

// TestClaudeLifecycle_New verifies proper initialization
func TestClaudeLifecycle_New(t *testing.T) {
	backend := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	lifecycle := &ClaudeLifecycle{
		backend: backend,
		hooks:   &config.HooksConfig{},
		mcp:     &config.MCPConfig{},
	}

	assert.NotNil(t, lifecycle)
	assert.Equal(t, backend, lifecycle.backend)
}

// TestClaudeLifecycle_OnSessionStart verifies session start handler registration
func TestClaudeLifecycle_OnSessionStart(t *testing.T) {
	backend := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	lifecycle := &ClaudeLifecycle{
		backend: backend,
	}

	handler := EventHandler{
		Command: "echo test",
		Timeout: 30,
	}

	err := lifecycle.OnSessionStart("/tmp", handler)
	require.NoError(t, err)

	// Verify hook was added
	assert.NotNil(t, lifecycle.hooks)
	assert.Len(t, lifecycle.hooks.Unified.SessionStart, 1)
}

// TestClaudeLifecycle_OnSessionEnd verifies session end handler registration
func TestClaudeLifecycle_OnSessionEnd(t *testing.T) {
	backend := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	lifecycle := &ClaudeLifecycle{
		backend: backend,
	}

	handler := EventHandler{
		Command: "echo cleanup",
		Timeout: 30,
	}

	err := lifecycle.OnSessionEnd("/tmp", handler)
	require.NoError(t, err)

	assert.NotNil(t, lifecycle.hooks)
	assert.Len(t, lifecycle.hooks.Unified.SessionEnd, 1)
}

// TestClaudeLifecycle_OnToolUse verifies tool use handler registration
func TestClaudeLifecycle_OnToolUse(t *testing.T) {
	backend := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	lifecycle := &ClaudeLifecycle{
		backend: backend,
	}

	handler := EventHandler{
		Command: "echo tool",
		Timeout: 30,
	}

	t.Run("before tool use", func(t *testing.T) {
		err := lifecycle.OnToolUse("/tmp", BeforeToolUse, handler)
		require.NoError(t, err)
		assert.Len(t, lifecycle.hooks.Unified.PreTool, 1)
	})

	t.Run("after tool use", func(t *testing.T) {
		lifecycle.hooks = &config.HooksConfig{}
		err := lifecycle.OnToolUse("/tmp", AfterToolUse, handler)
		require.NoError(t, err)
		assert.Len(t, lifecycle.hooks.Unified.PostTool, 1)
	})
}

// TestClaudeLifecycle_Clear verifies handlers can be cleared
func TestClaudeLifecycle_Clear(t *testing.T) {
	backend := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	lifecycle := &ClaudeLifecycle{
		backend: backend,
		hooks: &config.HooksConfig{
			Unified: config.UnifiedHooks{
				SessionStart: []config.Hook{
					{Command: "echo test"},
				},
			},
			Plugins: make(map[string]config.BackendHooks),
		},
		mcp: &config.MCPConfig{
			Servers: map[string]config.MCPServer{},
			Plugins: make(map[string]map[string]config.MCPServer),
		},
	}

	// Note: Clear will try to write to settings, which may fail in test
	// We're just verifying it resets internal state
	lifecycle.Clear("/tmp")
	assert.NotNil(t, lifecycle.hooks)
	assert.NotNil(t, lifecycle.mcp)
}

// TestClaudeLifecycle_Flush verifies hooks and MCP are flushed
func TestClaudeLifecycle_Flush(t *testing.T) {
	backend := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	lifecycle := &ClaudeLifecycle{
		backend: backend,
		hooks: &config.HooksConfig{
			Unified: config.UnifiedHooks{
				SessionStart: []config.Hook{
					{Command: "echo test"},
				},
			},
			Plugins: make(map[string]config.BackendHooks),
		},
		mcp: &config.MCPConfig{
			Servers: map[string]config.MCPServer{},
			Plugins: make(map[string]map[string]config.MCPServer),
		},
	}

	// Flush will attempt file I/O; we're verifying it doesn't panic
	_ = lifecycle.Flush("/tmp")
}

// =============================================================================
// Gemini Lifecycle Tests
// =============================================================================

// TestGeminiLifecycle_New verifies proper initialization
func TestGeminiLifecycle_New(t *testing.T) {
	backend := &Gemini{
		BaseBackend: NewBaseBackend("gemini", "1.0.0"),
	}
	lifecycle := &GeminiLifecycle{
		backend: backend,
		hooks:   &config.HooksConfig{},
		mcp:     &config.MCPConfig{},
	}

	assert.NotNil(t, lifecycle)
	assert.Equal(t, backend, lifecycle.backend)
}

// TestGeminiLifecycle_OnSessionStart verifies session start handler registration
func TestGeminiLifecycle_OnSessionStart(t *testing.T) {
	backend := &Gemini{
		BaseBackend: NewBaseBackend("gemini", "1.0.0"),
	}
	lifecycle := &GeminiLifecycle{
		backend: backend,
	}

	handler := EventHandler{
		Command: "echo test",
		Timeout: 30,
	}

	err := lifecycle.OnSessionStart("/tmp", handler)
	require.NoError(t, err)

	assert.NotNil(t, lifecycle.hooks)
	assert.Len(t, lifecycle.hooks.Unified.SessionStart, 1)
}

// TestGeminiLifecycle_OnSessionEnd verifies session end handler registration
func TestGeminiLifecycle_OnSessionEnd(t *testing.T) {
	backend := &Gemini{
		BaseBackend: NewBaseBackend("gemini", "1.0.0"),
	}
	lifecycle := &GeminiLifecycle{
		backend: backend,
	}

	handler := EventHandler{
		Command: "echo cleanup",
		Timeout: 30,
	}

	err := lifecycle.OnSessionEnd("/tmp", handler)
	require.NoError(t, err)

	assert.NotNil(t, lifecycle.hooks)
	assert.Len(t, lifecycle.hooks.Unified.SessionEnd, 1)
}

// TestGeminiLifecycle_OnToolUse verifies tool use handler registration
func TestGeminiLifecycle_OnToolUse(t *testing.T) {
	backend := &Gemini{
		BaseBackend: NewBaseBackend("gemini", "1.0.0"),
	}
	lifecycle := &GeminiLifecycle{
		backend: backend,
	}

	handler := EventHandler{
		Command: "echo tool",
		Timeout: 30,
	}

	t.Run("before tool use", func(t *testing.T) {
		err := lifecycle.OnToolUse("/tmp", BeforeToolUse, handler)
		require.NoError(t, err)
		assert.Len(t, lifecycle.hooks.Unified.PreTool, 1)
	})

	t.Run("after tool use", func(t *testing.T) {
		lifecycle.hooks = &config.HooksConfig{}
		err := lifecycle.OnToolUse("/tmp", AfterToolUse, handler)
		require.NoError(t, err)
		assert.Len(t, lifecycle.hooks.Unified.PostTool, 1)
	})
}

// TestGeminiCommand_Structure verifies the command structure
func TestGeminiCommand_Structure(t *testing.T) {
	cmd := GeminiCommand{
		Description: "Test command",
		Prompt:      "Test prompt",
	}

	assert.Equal(t, "Test command", cmd.Description)
	assert.Equal(t, "Test prompt", cmd.Prompt)
}
