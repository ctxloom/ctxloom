package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestReadPlanFiles_ReadsTheOutputDir is the guard on the second-order failure
// a write location creates: every session is told to write its plans in its
// output dir, and a reader looking anywhere else would return an empty list —
// which the distill, the cross-agent handoff and the artifact report all fold
// straight into their output, invisibly, with no error anywhere.
//
// So the assertion is on CONTENT reaching the caller, not on a count: an entry
// with the right name and an empty body would be the same silent loss wearing
// the shape of a success.
func TestReadPlanFiles_ReadsTheOutputDir(t *testing.T) {
	testsupport.Isolate(t)
	m, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/src/widget", "claude-code")
	require.NoError(t, err)
	out, err := m.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "design"+paths.PlanFileExt), []byte("# the decision"), 0o644))

	got := ReadPlanFiles(e.HarpName)
	require.Len(t, got, 1, "a plan written where the agent was told to write it must reach the reader")
	assert.Equal(t, "design", got[0].Name)
	assert.Equal(t, "# the decision", got[0].Content, "with its body, not an empty entry at the right name")
}
