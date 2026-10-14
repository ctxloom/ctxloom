package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionInstructions_PlanDirIsTheOutputDir pins the sentence that
// CREATES the population of plan files. Every session is told right here where
// to put its plans, so it must name the session's output dir — where a human
// reads them and a containerized run's writes reach the host — and never the
// machine session dir.
//
// The assertion is on the DIRECTORY the instruction hands out, not on prose,
// and both halves are needed: naming the output dir while still also offering
// the session dir would leave the agent free to pick the wrong one.
func TestSessionInstructions_PlanDirIsTheOutputDir(t *testing.T) {
	testsupport.Isolate(t)
	m, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/src/widget", "claude-code")
	require.NoError(t, err)
	planDir, err := m.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)
	harpDir, err := paths.HarpDir(e.HarpName)
	require.NoError(t, err)

	require.NoDirExists(t, planDir, "precondition: the mint records the output dir without creating it")

	got := SessionInstructions(afero.NewOsFs(), e.HarpName)

	assert.Contains(t, got, "`"+planDir+"`", "the instruction names the session's output dir")
	assert.DirExists(t, planDir, "the plan directory the instruction names exists before the agent's first write")
	assert.Contains(t, got, "`v1-removal"+paths.PlanFileExt+"`", "the worked example names a file in that directory")
	assert.NotContains(t, got, "`"+harpDir+"`",
		"the machine session dir must not be offered as a place to write")
}

// In a container the output dir is mounted at a path the sidecar does not
// record, and the container says so (CTXLOOM_OUTPUT_DIR): the instruction
// names the path the agent can actually write.
//
// A temp path stands in for the mount point: the test host cannot create
// /ctxloom/out, and the instruction names only a directory that exists.
func TestSessionInstructions_PlanDirInAContainerIsTheMountedOne(t *testing.T) {
	testsupport.Isolate(t)
	mounted := filepath.Join(t.TempDir(), "ctxloom", "out")
	t.Setenv(sessions.EnvOutputDir, mounted)
	got := SessionInstructions(afero.NewOsFs(), "brisk-teal-otter")
	assert.Contains(t, got, "`"+mounted+"`")
	assert.DirExists(t, mounted, "the plan directory exists where the agent reaches it")
}

// A plan directory that cannot be created is not named: the sentence would
// send the agent's first plan write to a path that is not there.
func TestSessionInstructions_UncreatablePlanDirIsNotNamed(t *testing.T) {
	testsupport.Isolate(t)
	blocker := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	t.Setenv(sessions.EnvOutputDir, filepath.Join(blocker, "out"))
	got := SessionInstructions(afero.NewOsFs(), "brisk-teal-otter")
	assert.Contains(t, got, "`brisk-teal-otter`", "the session is still named")
	assert.NotContains(t, got, paths.PlanFileExt, "no plan directory is offered")
}

// TestSessionInstructions_NoHarpAddsNoPlanDir: a caller with no session
// identity gets the bare server instructions. There is no harp to resolve a
// plan directory from, and inventing one would point an agent at a path no
// session owns.
func TestSessionInstructions_NoHarpAddsNoPlanDir(t *testing.T) {
	testsupport.Isolate(t)
	got := SessionInstructions(afero.NewOsFs(), "")
	// Still an EQUALITY, not a Contains: that is what catches a session-specific
	// addition leaking into the identity-less arm. The premise catalog is named
	// explicitly because it is session-INDEPENDENT — every caller gets it, harp
	// or no harp — so admitting it here weakens nothing.
	assert.Equal(t, strings.TrimRight(premiseCatalogInstruction(), "\n")+"\n\n"+strings.TrimRight(mcpServerInstructions, "\n"), got)
	assert.NotContains(t, got, paths.PlanFileExt)
}

// The plan directory is created through the filesystem the caller hands in,
// not beside it: on a memory filesystem it exists there and nowhere on disk.
func TestSessionInstructions_PlanDirIsCreatedThroughTheHandedFs(t *testing.T) {
	testsupport.Isolate(t)
	mounted := filepath.Join(t.TempDir(), "ctxloom", "out")
	t.Setenv(sessions.EnvOutputDir, mounted)
	fs := afero.NewMemMapFs()
	got := SessionInstructions(fs, "brisk-teal-otter")
	assert.Contains(t, got, "`"+mounted+"`")
	exists, err := afero.DirExists(fs, mounted)
	require.NoError(t, err)
	assert.True(t, exists, "the plan directory is created on the handed filesystem")
	assert.NoDirExists(t, mounted, "and not on the OS filesystem beside it")
}
