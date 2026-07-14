package claude

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestClaudeCodeHookWriter_WriteContext exercises the ContextWriter facet
// directly: the assembled context lands in the ctxloom-managed section of
// <projectDir>/CLAUDE.md and the report names it.
func TestClaudeCodeHookWriter_WriteContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	report, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/project", Context: "# Rules\nthe secret color is vermilion"})
	require.NoError(t, err)
	assert.Equal(t, []string{"CLAUDE.md"}, report.Wrote)
	assert.Empty(t, report.Removed)

	data, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), "the secret color is vermilion")
}

// TestClaudeCodeHookWriter_WriteContext_PreservesHandWrittenContent is the
// lanky-plop regression test (P0 data loss): a team's hand-authored CLAUDE.md
// must survive a WriteContext call byte-for-byte outside the managed markers.
// On the pre-fix code (bare afero.WriteFile of the whole file) this fails
// because "always use tabs" is gone — the whole file was clobbered.
func TestClaudeCodeHookWriter_WriteContext_PreservesHandWrittenContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}
	handWritten := "# Team conventions\nalways use tabs, never spaces\n"
	require.NoError(t, afero.WriteFile(fs, "/project/CLAUDE.md", []byte(handWritten), 0644))

	report, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/project", Context: "the secret color is vermilion"})
	require.NoError(t, err)
	assert.Equal(t, []string{"CLAUDE.md"}, report.Wrote)

	data, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), "always use tabs, never spaces", "hand-written content must survive outside the managed markers")
	assert.Contains(t, string(data), "the secret color is vermilion", "ctxloom's assembled context is still delivered")

	// Idempotence: applying again produces byte-identical output.
	data2, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	report2, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/project", Context: "the secret color is vermilion"})
	require.NoError(t, err)
	assert.Equal(t, []string{"CLAUDE.md"}, report2.Wrote)
	data3, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, string(data2), string(data3), "re-applying the same content is byte-identical")
}

// TestClaudeCodeHookWriter_WriteContextRemovesWhollyManaged verifies that when
// CLAUDE.md was wholly ctxloom's (no hand-written content), empty content
// removes the file entirely and reports Removed.
func TestClaudeCodeHookWriter_WriteContextRemovesWhollyManaged(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	_, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/p", Context: "managed only"})
	require.NoError(t, err)

	report, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/p", Context: ""})
	require.NoError(t, err)
	assert.Equal(t, []string{"CLAUDE.md"}, report.Removed)
	assert.Empty(t, report.Wrote)

	exists, _ := afero.Exists(fs, "/p/CLAUDE.md")
	assert.False(t, exists, "a wholly managed CLAUDE.md is removed on empty content")
}
