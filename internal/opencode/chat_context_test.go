//go:build parked_engines

package opencode

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// sick-dairy: opencode's structured-chat/oneshot path delivered model, MCP and
// permission through the transient opencode.json overlay but NOT the context
// surface (instructions[] -> .opencode/ctxloom-context.md). Context delivery was
// wired only to the persistent materialize + interactive paths, so
// `opencode acp` runs saw NO project context at all — a silent functional gap:
// the run succeeds, the agent just knows nothing about the project.
func TestChatManaged_DeliversContextSurface(t *testing.T) {
	mcp := []agent.ChatMCPServer{{Name: "ctxloom"}}

	t.Run("assembled context rides the chat overlay as instructions", func(t *testing.T) {
		mc := chatManaged(agent.ChatRequest{}, "some-model", "ASSEMBLED CONTEXT", mcp)
		assert.Equal(t, []string{opencodeContextFile}, mc.instructions,
			"chat must point opencode at the ctxloom context file, exactly as the interactive path does")
		assert.Equal(t, "some-model", mc.model)
		assert.Equal(t, mcp, mc.mcpServers)
		assert.False(t, mc.readOnly)
	})

	t.Run("no assembled context means no instructions key", func(t *testing.T) {
		mc := chatManaged(agent.ChatRequest{}, "some-model", "", mcp)
		assert.Empty(t, mc.instructions)
	})

	t.Run("plan permissions stay read-only and still get context", func(t *testing.T) {
		mc := chatManaged(agent.ChatRequest{Permissions: agent.PermissionPlan}, "m", "CTX", mcp)
		assert.True(t, mc.readOnly)
		assert.Equal(t, []string{opencodeContextFile}, mc.instructions)
	})
}

// TestMaterializeContextSurface proves the file the instructions key points at
// is actually written, and reverted — a transient overlay must leave no debris.
func TestMaterializeContextSurface(t *testing.T) {
	fs := afero.NewMemMapFs()
	workDir := "/proj"
	ctxPath := filepath.Join(workDir, opencodeContextFile)

	remove, err := materializeContextSurface(fs, workDir, "SENTINEL-CONTEXT-abc123")
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, ctxPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "SENTINEL-CONTEXT-abc123")

	require.NoError(t, remove())
	exists, err := afero.Exists(fs, ctxPath)
	require.NoError(t, err)
	assert.False(t, exists, "the transient context file must not survive the run")
}

// TestMaterializeContextSurface_EmptyIsNoOp keeps the no-context case from
// writing an empty instruction file the model would then be pointed at.
func TestMaterializeContextSurface_EmptyIsNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()
	remove, err := materializeContextSurface(fs, "/proj", "")
	require.NoError(t, err)
	require.NoError(t, remove())

	exists, err := afero.Exists(fs, filepath.Join("/proj", opencodeContextFile))
	require.NoError(t, err)
	assert.False(t, exists)
}
