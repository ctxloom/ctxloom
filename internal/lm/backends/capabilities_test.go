package backends

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeminiLifecycle_New verifies proper initialization
func TestGeminiLifecycle_New(t *testing.T) {
	backend := NewGemini(WriteSettings)
	lifecycle := NewGeminiLifecycle(backend)

	assert.NotNil(t, lifecycle)
	assert.Equal(t, backend, lifecycle.backend)
	assert.NotNil(t, lifecycle.BaseLifecycle)
}

// TestGeminiLifecycle_OnSessionStart verifies session start handler registration
func TestGeminiLifecycle_OnSessionStart(t *testing.T) {
	backend := NewGemini(WriteSettings)
	lifecycle := NewGeminiLifecycle(backend)

	handler := EventHandler{
		Command: "echo test",
		Timeout: 30,
	}

	err := lifecycle.OnSessionStart("/tmp", handler)
	require.NoError(t, err)

	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionStart, 1)
}

// TestGeminiLifecycle_OnSessionEnd verifies session end handler registration
func TestGeminiLifecycle_OnSessionEnd(t *testing.T) {
	backend := NewGemini(WriteSettings)
	lifecycle := NewGeminiLifecycle(backend)

	handler := EventHandler{
		Command: "echo cleanup",
		Timeout: 30,
	}

	err := lifecycle.OnSessionEnd("/tmp", handler)
	require.NoError(t, err)

	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionEnd, 1)
}

// TestGeminiLifecycle_OnToolUse verifies tool use handler registration
func TestGeminiLifecycle_OnToolUse(t *testing.T) {
	backend := NewGemini(WriteSettings)
	lifecycle := NewGeminiLifecycle(backend)

	handler := EventHandler{
		Command: "echo tool",
		Timeout: 30,
	}

	t.Run("before tool use", func(t *testing.T) {
		err := lifecycle.OnToolUse("/tmp", BeforeToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle.GetHooks()
		assert.Len(t, hooks.Unified.PreTool, 1)
	})

	t.Run("after tool use", func(t *testing.T) {
		lifecycle2 := NewGeminiLifecycle(backend)
		err := lifecycle2.OnToolUse("/tmp", AfterToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle2.GetHooks()
		assert.Len(t, hooks.Unified.PostTool, 1)
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

func TestGeminiLifecycle_GetMCP(t *testing.T) {
	backend := NewGemini(WriteSettings)
	lifecycle := NewGeminiLifecycle(backend)

	// Initially nil
	mcp := lifecycle.GetMCP()
	assert.Nil(t, mcp)

	// After merging config with MCP servers
	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"test-server": {Command: "test"},
			},
			Plugins: make(map[string]map[string]config.MCPServer),
		},
	}
	lifecycle.MergeConfigHooks(cfg, "/tmp", "")

	mcp = lifecycle.GetMCP()
	assert.NotNil(t, mcp)
}

func TestGemini_History(t *testing.T) {
	backend := NewGemini(WriteSettings)
	history := backend.History()
	assert.NotNil(t, history)
}

func TestCodex_History(t *testing.T) {
	backend := NewCodex()
	history := backend.History()
	assert.NotNil(t, history)
}

func TestMock_History(t *testing.T) {
	backend := NewMock()
	history := backend.History()
	// Mock returns a NilSessionHistory (stub that returns empty/nil for all methods)
	assert.NotNil(t, history)
}
