package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test all backend constructors and their stub methods

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
