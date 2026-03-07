// Package backends tests verify the LLM backend implementations.
//
// Backends are adapters that translate SCM configuration into the format
// expected by different AI coding tools (Claude Code, Gemini, Aider, etc.).
//
// # Backend Capabilities
//
// Each backend implements some subset of these capabilities:
//   - Lifecycle: Process management (start, stop, health checks)
//   - Skills: Custom commands/slash commands
//   - Context: Reading and injecting context fragments
//   - MCP: Model Context Protocol server integration
//   - Hooks: Pre/post tool execution hooks
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

func TestNewAider(t *testing.T) {
	backend := NewAider()
	assert.Equal(t, "aider", backend.Name())
	assert.Nil(t, backend.Lifecycle())
	assert.Nil(t, backend.Skills())
	assert.NotNil(t, backend.Context()) // Uses CLIContextProvider
	assert.Nil(t, backend.MCP())
}

func TestNewCline(t *testing.T) {
	backend := NewCline()
	assert.Equal(t, "cline", backend.Name())
	assert.Nil(t, backend.Lifecycle())
	assert.Nil(t, backend.Skills())
	assert.NotNil(t, backend.Context()) // Uses CLIContextProvider
	assert.Nil(t, backend.MCP())
}

func TestNewCodex(t *testing.T) {
	backend := NewCodex()
	assert.Equal(t, "codex", backend.Name())
	assert.Nil(t, backend.Lifecycle())
	assert.Nil(t, backend.Skills())
	assert.NotNil(t, backend.Context()) // Uses CLIContextProvider
	assert.Nil(t, backend.MCP())
}

func TestNewGoose(t *testing.T) {
	backend := NewGoose()
	assert.Equal(t, "goose", backend.Name())
	assert.Nil(t, backend.Lifecycle())
	assert.Nil(t, backend.Skills())
	assert.NotNil(t, backend.Context()) // Uses CLIContextProvider
	assert.Nil(t, backend.MCP())
}

func TestNewQDeveloper(t *testing.T) {
	backend := NewQDeveloper()
	assert.Equal(t, "q", backend.Name()) // Actually named "q" not "qdeveloper"
	assert.Nil(t, backend.Lifecycle())
	assert.Nil(t, backend.Skills())
	assert.NotNil(t, backend.Context()) // Uses CLIContextProvider
	assert.Nil(t, backend.MCP())
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
	assert.Nil(t, backend.Skills()) // Gemini doesn't support skills
	assert.NotNil(t, backend.Context())
	assert.NotNil(t, backend.MCP())
}
