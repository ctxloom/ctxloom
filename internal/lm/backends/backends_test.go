// Package backends tests verify the LLM backend implementations.
//
// Backends are adapters that translate ctxloom configuration into the format
// expected by different AI coding tools (Claude Code, Gemini, Codex).
//
// # Backend Capabilities
//
// Each backend implements some subset of these capabilities:
//   - Lifecycle: Process management (start, stop, health checks)
//   - Skills: Custom commands/slash commands
//   - Context: Reading and injecting context fragments
//   - MCP: Model Context Protocol server integration
//   - Hooks: Pre/post tool execution hooks
//   - History: Session transcript access
//
// # Testing Approach
//
// These tests verify:
//   - Constructor returns properly named backends
//   - Capability providers are correctly wired (nil vs not-nil)
//   - Settings file generation produces valid JSON/YAML
//
// # Integration vs Unit Tests
//
// Most backend tests are unit tests using dependency injection.
// Full integration tests (actually running Claude/Gemini) are separate
// and require real API credentials.
package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Backend Constructor Tests
// =============================================================================
//
// These tests verify that each backend constructor returns a properly
// configured backend with the expected capabilities.

func TestNewCodex(t *testing.T) {
	backend := NewCodex()
	assert.Equal(t, "codex", backend.Name())
	assert.Nil(t, backend.Lifecycle())
	assert.Nil(t, backend.Skills())
	assert.NotNil(t, backend.Context()) // Uses CLIContextProvider
	assert.Nil(t, backend.MCP())
	assert.NotNil(t, backend.History()) // Supports session history
}

func TestNewClaudeCode(t *testing.T) {
	backend := NewClaudeCode()
	assert.Equal(t, "claude-code", backend.Name())
	assert.NotNil(t, backend.Lifecycle())
	assert.NotNil(t, backend.Skills())
	assert.NotNil(t, backend.Context())
	assert.NotNil(t, backend.MCP())
}

func TestNewGemini(t *testing.T) {
	backend := NewGemini()
	assert.Equal(t, "gemini", backend.Name())
	assert.NotNil(t, backend.Lifecycle())
	assert.NotNil(t, backend.Skills()) // Gemini now supports skills via TOML commands
	assert.NotNil(t, backend.Context())
	assert.NotNil(t, backend.MCP())
}
