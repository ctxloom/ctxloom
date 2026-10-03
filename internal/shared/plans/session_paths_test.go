package plans

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

func writePlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name+paths.PlanFileExt)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// listRoot is ListSessions over a fixture laid out as <root>/<harp>/: each
// harp directory is that session's output dir.
func listRoot(t *testing.T, root string) ([]Plan, error) {
	t.Helper()
	dirs, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var entries []sessions.Entry
	for _, d := range dirs {
		if d.IsDir() {
			entries = append(entries, sessions.Entry{HarpName: d.Name(), OutputDir: filepath.Join(root, d.Name())})
		}
	}
	return ListSessions(entries)
}

// mintWithOutputDir mints a real session and records an output dir for it.
func mintWithOutputDir(t *testing.T) (harp, out string) {
	t.Helper()
	m, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/src/widget", "claude-code")
	require.NoError(t, err)
	out, err = m.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)
	return e.HarpName, out
}

// TestSessionPlanPaths_ReadsTheRecordedOutputDir: a session's plans are the
// *.plan.md files at the top of its recorded output dir, sorted; other files
// are not plans.
func TestSessionPlanPaths_ReadsTheRecordedOutputDir(t *testing.T) {
	testsupport.Isolate(t)
	harp, out := mintWithOutputDir(t)
	zeta := writePlan(t, out, "zeta", "# zeta")
	alpha := writePlan(t, out, "alpha", "# alpha")
	require.NoError(t, os.WriteFile(filepath.Join(out, paths.EssenceFileName), []byte("not a plan"), 0o644))

	got, problems := SessionPlanPaths(harp)
	assert.Empty(t, problems)
	assert.Equal(t, []string{alpha, zeta}, got)
}

// TestSessionPlanPaths_IsNotRecursive: the output dir's subdirectories hold
// published reports and segment essences, which are not this session's plans.
func TestSessionPlanPaths_IsNotRecursive(t *testing.T) {
	testsupport.Isolate(t)
	harp, out := mintWithOutputDir(t)
	mine := writePlan(t, out, "design", "# mine")
	writePlan(t, filepath.Join(out, paths.OutputReportsDirName), "published", "# a report")

	got, problems := SessionPlanPaths(harp)
	assert.Empty(t, problems)
	assert.Equal(t, []string{mine}, got)
}

// TestSessionPlanPaths_EmptyHarpAndNoOutputDir: an empty harp, or one that is
// no session, is no fault; a session with no recorded output dir IS one — its
// plans could be nowhere a reader looks, which must not read as "authored
// none".
func TestSessionPlanPaths_EmptyHarpAndNoOutputDir(t *testing.T) {
	testsupport.Isolate(t)
	got, problems := SessionPlanPaths("")
	assert.Nil(t, got)
	assert.Empty(t, problems)
	got, problems = SessionPlanPaths("never-minted-harp")
	assert.Nil(t, got)
	assert.Empty(t, problems)

	m, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/src/widget", "claude-code")
	require.NoError(t, err)
	got, problems = SessionPlanPaths(e.HarpName)
	assert.Nil(t, got)
	require.Len(t, problems, 1)
	assert.ErrorIs(t, problems[0], sessions.ErrNoOutputDir)
}

// TestListSessions_NamesKeepTheirNesting: a nested plan keeps its
// subdirectory in its name, because that IS a distinction.
func TestListSessions_NamesKeepTheirNesting(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "brisk-teal-otter")
	writePlan(t, out, "design", "# top")
	writePlan(t, filepath.Join(out, "archive"), "old", "# nested")

	got, err := listRoot(t, root)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "archive/old", got[0].Name)
	assert.Equal(t, "design", got[1].Name)
	assert.Equal(t, "brisk-teal-otter", got[1].Session)
}
