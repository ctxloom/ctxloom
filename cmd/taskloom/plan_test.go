package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// seedPlanWorld lays down plan files plus the session index that attributes
// them, inside an isolated home. Returns the home dir.
func seedPlanWorld(t *testing.T, harpToDir map[string]string, orphanHarps ...string) string {
	t.Helper()
	home := taskstest.Isolate(t)
	body := "sessions:\n"
	for harp, dir := range harpToDir {
		writePlanFile(t, home, harp)
		body += "    - harp_name: " + harp + "\n      project_dir: " + dir + "\n"
	}
	for _, harp := range orphanHarps {
		writePlanFile(t, home, harp)
	}
	writeFile(t, filepath.Join(home, ".ctxloom", "sessions", "index.yaml"), body)
	return home
}

func writePlanFile(t *testing.T, home, harp string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".ctxloom", "sessions", harp, "design.plan.md"),
		"---\ntitle: "+harp+" plan\n---\nbody")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// The default listing must be scoped to the current project — not to every
// session on the machine.
func TestRunPlanList_DefaultScopesToCurrentProject(t *testing.T) {
	dir := t.TempDir()
	seedPlanWorld(t, map[string]string{"mine": dir, "theirs": "/somewhere/else"})
	gitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	var out, errw bytes.Buffer
	tc := operations.TaskContext{WorkDir: dir}
	require.NoError(t, runPlanListCmd(&out, &errw, tc, planListOptions{}))

	assert.Contains(t, out.String(), "mine")
	assert.NotContains(t, out.String(), "theirs",
		"another project's plans must not appear in a scoped listing")
}

// --global widens back to every session.
func TestRunPlanList_GlobalShowsEveryProject(t *testing.T) {
	dir := t.TempDir()
	seedPlanWorld(t, map[string]string{"mine": dir, "theirs": "/somewhere/else"})
	gitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	var out, errw bytes.Buffer
	tc := operations.TaskContext{WorkDir: dir}
	require.NoError(t, runPlanListCmd(&out, &errw, tc, planListOptions{Global: true}))

	assert.Contains(t, out.String(), "mine")
	assert.Contains(t, out.String(), "theirs")
}

// A plan whose session is missing from the index must appear in BOTH the
// scoped and the global listing, marked — never silently dropped.
func TestRunPlanList_UnattributedPlansAlwaysShown(t *testing.T) {
	dir := t.TempDir()
	seedPlanWorld(t, map[string]string{"mine": dir, "theirs": "/somewhere/else"}, "orphan")
	gitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	tc := operations.TaskContext{WorkDir: dir}

	var scoped, errw bytes.Buffer
	require.NoError(t, runPlanListCmd(&scoped, &errw, tc, planListOptions{}))
	assert.Contains(t, scoped.String(), "orphan",
		"an unattributable plan must survive a scoped listing")

	var global, errw2 bytes.Buffer
	require.NoError(t, runPlanListCmd(&global, &errw2, tc, planListOptions{Global: true}))
	assert.Contains(t, global.String(), "orphan")
}

// With no project resolvable at all, the listing falls back to global and says
// so on stderr — matching `taskloom list` / task_list.
func TestRunPlanList_NoProjectFallsBackToGlobalWithNotice(t *testing.T) {
	dir := t.TempDir() // not a git repo, no marker, no registry entry
	seedPlanWorld(t, map[string]string{"mine": "/work/alpha", "theirs": "/work/beta"})
	taskstest.ChangeDir(t, dir)

	var out, errw bytes.Buffer
	tc := operations.TaskContext{WorkDir: dir}
	require.NoError(t, runPlanListCmd(&out, &errw, tc, planListOptions{}))

	assert.Contains(t, out.String(), "mine")
	assert.Contains(t, out.String(), "theirs", "fallback must widen to global")
	assert.NotEmpty(t, errw.String(), "the fallback must be announced on stderr")
	assert.Contains(t, strings.ToLower(errw.String()), "project")
}

// `plan show` takes a PATH and nothing else, so a text listing that omits the
// path cannot be fed to it. Assert the two commands actually compose: take a
// path straight out of `plan list`'s text output and hand it to plan show.
func TestRunPlanList_TextRowsCarryThePathPlanShowAccepts(t *testing.T) {
	dir := t.TempDir()
	seedPlanWorld(t, map[string]string{"mine": dir}, "orphan")
	gitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	var out, errw bytes.Buffer
	tc := operations.TaskContext{WorkDir: dir}
	require.NoError(t, runPlanListCmd(&out, &errw, tc, planListOptions{}))

	rows := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, rows, 2)
	for _, row := range rows {
		cols := strings.Split(row, "\t")
		require.Len(t, cols, 5, "row %q must carry the path column", row)

		// The unchanged prefix: adding a column must not renumber the others.
		assert.Equal(t, "design", cols[1], "name stays column 2")

		content, err := plans.Show(cols[4])
		require.NoError(t, err, "the listed path must be one plan show accepts")
		assert.Contains(t, content, "body")
	}
}

// --format json still serializes the rows, now carrying project attribution.
func TestRunPlanList_JSONFormatHonoured(t *testing.T) {
	dir := t.TempDir()
	seedPlanWorld(t, map[string]string{"mine": dir, "theirs": "/somewhere/else"}, "orphan")
	gitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	var out, errw bytes.Buffer
	tc := operations.TaskContext{WorkDir: dir}
	require.NoError(t, runPlanListCmd(&out, &errw, tc, planListOptions{Format: clifmt.FormatJSON}))

	var got []plans.Plan
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "output must be valid JSON")
	require.Len(t, got, 2, "the scoped project's plan plus the unattributed one")

	bySession := map[string]plans.Plan{}
	for _, p := range got {
		bySession[p.Session] = p
	}
	assert.Equal(t, filepath.Clean(dir), bySession["mine"].ProjectDir)
	assert.Empty(t, bySession["orphan"].ProjectDir, "unattributed carries no project dir")
}
