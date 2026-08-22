package grpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestReadPlanFiles_ReadsThePlanDir is the guard on the second-order failure
// that repointing the write location creates. mcp.sessionInstructions now
// sends every plan to the harp's persist dir; this reader used to list the
// harp TOP LEVEL and nothing else. Left that way it would have returned an
// empty list for every plan authored after the repoint, and distill, the
// cross-agent handoff and the artifact report all fold an empty plan list
// straight into their output — the omission is invisible forever after, with
// no error anywhere.
//
// So the assertion is on CONTENT reaching the caller, not on a count: an entry
// with the right name and an empty body would be the same silent loss wearing
// the shape of a success.
func TestReadPlanFiles_ReadsThePlanDir(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "design"+paths.PlanFileExt), []byte("# the decision"), 0o644))

	got := ReadPlanFiles(harp)
	require.Len(t, got, 1, "a plan written where the agent was told to write it must reach the reader")
	assert.Equal(t, "design", got[0].Name)
	assert.Equal(t, "# the decision", got[0].Content, "with its body, not an empty entry at the right name")
}

// TestReadPlanFiles_PlanDirAndLegacyTopLevelTogether: a harp part-way through
// migration has plans in both places, and a distill that dropped either half
// would report a session's design record as smaller than it is.
func TestReadPlanFiles_PlanDirAndLegacyTopLevelTogether(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "zeta"+paths.PlanFileExt), []byte("z"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "alpha"+paths.PlanFileExt), []byte("a"), 0o644))

	got := ReadPlanFiles(harp)
	require.Len(t, got, 2)
	assert.Equal(t, "alpha", got[0].Name)
	assert.Equal(t, "a", got[0].Content)
	assert.Equal(t, "zeta", got[1].Name)
	assert.Equal(t, "z", got[1].Content)
}
