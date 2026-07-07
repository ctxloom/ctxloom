package claude

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestClaudeCodeHookWriter_WriteContext exercises the ContextWriter facet
// directly: the assembled context lands verbatim in <projectDir>/CLAUDE.md
// (raw, whole-file, no markers) and the report names it.
func TestClaudeCodeHookWriter_WriteContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	report, err := writer.WriteContext(agent.ContextWriteRequest{ProjectDir: "/project", Context: "# Rules\nthe secret color is vermilion"})
	require.NoError(t, err)
	assert.Equal(t, []string{"CLAUDE.md"}, report.Wrote)
	assert.Empty(t, report.Removed)

	data, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	// Raw, whole-file content: byte-identical to the input, no marker framing.
	assert.Equal(t, "# Rules\nthe secret color is vermilion", string(data))
}
