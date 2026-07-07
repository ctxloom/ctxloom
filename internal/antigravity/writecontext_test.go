package antigravity

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestAntigravityHookWriter_WriteContext exercises the ContextWriter facet
// directly (string in → AGENTS.md surface out, no hash indirection): the
// managed section carries the content, user content outside the markers is
// preserved, and the report names the workspace-relative path.
func TestAntigravityHookWriter_WriteContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/AGENTS.md", []byte("# My rules\nalways frobnicate\n"), 0644))

	report, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/project", Context: "the secret color is vermilion"})
	require.NoError(t, err)
	assert.Equal(t, []string{".agents/AGENTS.md"}, report.Wrote)
	assert.Empty(t, report.Removed)

	data, err := afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), managedContextBegin)
	assert.Contains(t, string(data), "the secret color is vermilion")
	assert.Contains(t, string(data), managedContextEnd)
	assert.Contains(t, string(data), "always frobnicate", "user content outside markers preserved")

	// Empty content strips the managed section; the user-authored file survives,
	// so it is a write (of the section-less file), not a removal.
	report, err = writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/project", Context: ""})
	require.NoError(t, err)
	assert.Equal(t, []string{".agents/AGENTS.md"}, report.Wrote)
	data, _ = afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	assert.NotContains(t, string(data), managedContextBegin)
	assert.Contains(t, string(data), "always frobnicate")
}

// TestAntigravityHookWriter_WriteContextRemovesWhollyManaged verifies that when
// the file was wholly ctxloom's, empty content removes it and reports Removed.
func TestAntigravityHookWriter_WriteContextRemovesWhollyManaged(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	_, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/p", Context: "managed only"})
	require.NoError(t, err)

	report, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/p", Context: ""})
	require.NoError(t, err)
	assert.Equal(t, []string{".agents/AGENTS.md"}, report.Removed)
	assert.Empty(t, report.Wrote)

	exists, _ := afero.Exists(fs, "/p/.agents/AGENTS.md")
	assert.False(t, exists, "a wholly managed AGENTS.md is removed on empty content")
}
