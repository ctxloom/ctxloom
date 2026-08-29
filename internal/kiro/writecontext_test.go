//go:build parked_engines

package kiro

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestKiroWriter_WriteContext exercises the ContextWriter facet directly
// (string in → steering surface out, no hash indirection): the content lands in
// the ctxloom-owned steering file with the always-inclusion front-matter, empty
// content removes it, and the report names the workspace-relative path.
func TestKiroWriter_WriteContext(t *testing.T) {
	w, fs := newTestWriter()

	report, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj", Context: "PROJECT RULES"})
	require.NoError(t, err)
	assert.Equal(t, []string{".kiro/steering/ctxloom-context.md"}, report.Wrote)
	assert.Empty(t, report.Removed)

	steering, err := afero.ReadFile(fs, "/proj/.kiro/steering/ctxloom-context.md")
	require.NoError(t, err)
	assert.Contains(t, string(steering), "inclusion: always")
	assert.Contains(t, string(steering), "PROJECT RULES")

	// Empty content removes the steering file.
	report, err = w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj", Context: ""})
	require.NoError(t, err)
	assert.Equal(t, []string{".kiro/steering/ctxloom-context.md"}, report.Removed)
	assert.Empty(t, report.Wrote)

	exists, _ := afero.Exists(fs, "/proj/.kiro/steering/ctxloom-context.md")
	assert.False(t, exists)
}
