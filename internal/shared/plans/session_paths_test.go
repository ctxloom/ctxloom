package plans

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func writePlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name+paths.PlanFileExt)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// TestSessionPlanPaths_FindsThePlanDirAndTheLegacyTopLevel: the durable
// location is found, the pre-migration top level is still found, and the two
// come back sorted as one list. The top-level half is not nostalgia — a harp
// written before the instruction moved has all its plans there, and a reader
// that stopped looking would report those sessions as having authored nothing.
func TestSessionPlanPaths_FindsThePlanDirAndTheLegacyTopLevel(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)

	durable := writePlan(t, planDir, "zeta", "# zeta")
	legacy := writePlan(t, harpDir, "alpha", "# alpha")
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "notes.md"), []byte("not a plan"), 0o644))

	got, problems := SessionPlanPaths(harp)
	assert.Empty(t, problems)
	assert.Equal(t, []string{legacy, durable}, got, "both locations, sorted by base name, and notes.md is not a plan")
}

// TestSessionPlanPaths_PersistShadowsASameNamedTopLevelTwin: after a migration
// that could not move a file because persist/ already held that name, both
// copies exist. The durable one is the one that counts; returning both would
// hand the same plan name to a consumer twice with different bodies.
func TestSessionPlanPaths_PersistShadowsASameNamedTopLevelTwin(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)

	durable := writePlan(t, planDir, "design", "# the durable one")
	writePlan(t, harpDir, "design", "# the stale twin")

	got, problems := SessionPlanPaths(harp)
	assert.Empty(t, problems)
	require.Equal(t, []string{durable}, got, "persist/ wins; the top-level twin is not returned as a second plan")
	body, err := os.ReadFile(got[0])
	require.NoError(t, err)
	assert.Equal(t, "# the durable one", string(body))
}

// TestSessionPlanPaths_IgnoresEphemeralScratchWorktrees is why neither
// directory is walked recursively. <harp>/ephemeral holds checked-out git
// worktrees of the USER'S project; a recursive walk would pull every *.plan.md
// inside one into the session's plan list, and those files belong to the repo,
// not to the session.
func TestSessionPlanPaths_IgnoresEphemeralScratchWorktrees(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	ephemeral, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)

	mine := writePlan(t, planDir, "design", "# mine")
	writePlan(t, filepath.Join(ephemeral, "ctxloom-wt-abc", "docs"), "someone-elses", "# checked out")

	got, problems := SessionPlanPaths(harp)
	assert.Empty(t, problems)
	assert.Equal(t, []string{mine}, got)
}

// TestSessionPlanPaths_EmptyHarpAndMissingSession: neither is a fault, and
// neither reports a problem — a session that never authored a plan is not a
// session whose plans could not be read.
func TestSessionPlanPaths_EmptyHarpAndMissingSession(t *testing.T) {
	testsupport.Isolate(t)
	got, problems := SessionPlanPaths("")
	assert.Nil(t, got)
	assert.Empty(t, problems)

	got, problems = SessionPlanPaths("never-created")
	assert.Nil(t, got)
	assert.Empty(t, problems)
}

// TestList_PlanNameIsStableAcrossTheMigration: a plan keeps the name it had
// before the sweep moved it under persist/. Letting the directory show through
// would rename every plan in every listing at the moment of migration —
// "design" becoming "persist/design" — for no distinction a reader can act on.
// A genuinely nested plan keeps its subdirectory, because that one IS a
// distinction.
func TestList_PlanNameIsStableAcrossTheMigration(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	writePlan(t, planDir, "design", "# after the sweep")
	writePlan(t, filepath.Join(planDir, "archive"), "old", "# nested")

	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	got, err := List(root)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "archive/old", got[0].Name, "real nesting survives in the name")
	assert.Equal(t, "design", got[1].Name, "the persist/ segment does not")
}
