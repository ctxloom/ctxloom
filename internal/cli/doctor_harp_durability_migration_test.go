package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestDoctorHarpDurability_MigrationClearsTheWarning closes the loop between
// the detector and the mover, which is the whole reason they share one
// predicate. A check that flags files nothing can move is a warning a user can
// never clear, and it trains them to ignore the report.
//
// So this asserts the sequence end to end: doctor warns, the sweep runs, the
// plan is READABLE at its durable path with its bytes intact, `ctxloom plan`
// still lists it, and doctor now says OK. The plan-listing half matters on its
// own — relocating a plan into a directory the plan lister cannot see would
// trade a durability bug for an invisibility one.
func TestDoctorHarpDurability_MigrationClearsTheWarning(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "amber-quiet-heron"
	const body = "---\ntitle: The Design\n---\n\nthe decision and why\n"
	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "design"+paths.PlanFileExt), []byte(body), 0o644))

	before := doctorCheckHarpDurability()
	require.Equal(t, doctorWarn, before.Status, "the undurable plan must be reported before anything moves it")
	require.Contains(t, before.Detail, "design"+paths.PlanFileExt)

	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	result, err := operations.MigrateHarpArtifacts(sessionsRoot, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Moved)

	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	moved, err := os.ReadFile(filepath.Join(planDir, "design"+paths.PlanFileExt))
	require.NoError(t, err)
	assert.Equal(t, body, string(moved), "the plan's bytes must survive the move")

	listed, err := plans.ListHome()
	require.NoError(t, err)
	require.Len(t, listed, 1, "`ctxloom plan` must still list a plan that moved under persist/")
	assert.Equal(t, harp, listed[0].Session)
	assert.Equal(t, "The Design", listed[0].Title, "frontmatter is still parsed at the new location")

	after := doctorCheckHarpDurability()
	assert.Equal(t, doctorOK, after.Status, "the check the mover exists to satisfy must actually go OK")
	assert.NotContains(t, after.Detail, "design"+paths.PlanFileExt)
}
