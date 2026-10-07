// Capability tests for the Claude Code launch backend's context provider.
package claude

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeContext_GetContextFilePath_Empty(t *testing.T) {
	context := agent.NewBaseContextProvider()

	path := context.GetContextFilePath()
	assert.Equal(t, "", path)
}

func TestClaudeContext_GetContextFilePath_WithHash(t *testing.T) {
	context := agent.NewBaseContextProvider()

	// Provide context to generate a hash
	tmpDir := t.TempDir()
	_ = context.Provide(tmpDir, []*agent.Fragment{{Content: "test content"}})

	path := context.GetContextFilePath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, filepath.FromSlash(agent.SCMContextSubdir), "the path is joined with the host separator")
	assert.Contains(t, path, ".md")
}

func TestClaudeContext_Clear(t *testing.T) {
	workDir := t.TempDir()
	context := agent.NewBaseContextProvider()

	// Provide some context first
	require.NoError(t, context.Provide(workDir, []*agent.Fragment{{Content: "test"}}))

	require.NotEmpty(t, context.GetContextFilePath(), "precondition: there is a context file to clear")
	err := context.Clear(workDir)
	require.NoError(t, err)
	assert.Equal(t, "", context.GetContextFilePath())
}
