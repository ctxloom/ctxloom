package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// gitInit runs `git init` against dir so workdir.ResolveBoundary() can find a
// real git root there — the boundary case resolveListScope must treat as a
// normal, project-scoped read (even on a repo's very first taskloom call).
func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--quiet", dir)
	require.NoError(t, cmd.Run(), "git init")
}

func TestResolveListScope_ExplicitGlobal_AlwaysAggregatesNoNotice(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()

	scope, err := resolveListScope(true, "pinned-project", dir, false)
	require.NoError(t, err)
	assert.True(t, scope.Global)
	assert.Empty(t, scope.Notice, "an explicit --global is a silent opt-in")
}

func TestResolveListScope_PinnedProjectID_StaysProjectScoped(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir() // not a git repo, not established — the pin alone must be enough

	scope, err := resolveListScope(false, "pinned-project", dir, false)
	require.NoError(t, err)
	assert.False(t, scope.Global)
	assert.Empty(t, scope.Notice)
}

func TestResolveListScope_GitBoundary_StaysProjectScopedEvenUnestablished(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()
	gitInit(t, dir)
	taskstest.ChangeDir(t, dir)

	// A git repo's first-ever taskloom call is still a real project: no prior
	// task history, no marker, but a real boundary.
	scope, err := resolveListScope(false, "", dir, true)
	require.NoError(t, err)
	assert.False(t, scope.Global)
	assert.Empty(t, scope.Notice)
}

func TestResolveListScope_NoGitNoHistory_FallsBackToGlobalWithNotice(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir() // no git, no marker, no registry entry
	taskstest.ChangeDir(t, dir)

	scope, err := resolveListScope(false, "", dir, false)
	require.NoError(t, err)
	assert.True(t, scope.Global, "no boundary and no established identity must default to global")
	require.NotEmpty(t, scope.Notice, "the fallback must be explained, not silent")
	assert.Contains(t, scope.Notice, "no project detected")
	assert.Contains(t, scope.Notice, dir)
}

func TestResolveListScope_EstablishedMarkerWithoutGit_StaysProjectScoped(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()
	taskstest.ChangeDir(t, dir)
	require.NoError(t, projectid.WriteMarker(dir, "adopted-project"))

	scope, err := resolveListScope(false, "", dir, false)
	require.NoError(t, err)
	assert.False(t, scope.Global, "an in-tree marker is an established identity even without git")
	assert.Empty(t, scope.Notice)
}

func TestIsEstablishedProject_RegistryEntryByPathCounts(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()

	pm, err := projectid.Open("")
	require.NoError(t, err)
	_, err = pm.Mint(dir) // registers dir by path without writing an in-tree marker

	require.NoError(t, err)

	got, err := isEstablishedProject(dir)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestIsEstablishedProject_UnknownDirIsNotEstablished(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()

	got, err := isEstablishedProject(dir)
	require.NoError(t, err)
	assert.False(t, got)
}

func TestFilterActiveDefault_HidesCompletedAndDeferredByDefault(t *testing.T) {
	list := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo},
		{HarpID: "b", Status: tasks.StatusDone, Checked: true},
		{HarpID: "c", Status: tasks.StatusDeferred},
		{HarpID: "d", Status: tasks.StatusInProgress},
	}

	visible, hiddenCompleted, hiddenDeferred := filterActiveDefault(list, false, nil)
	require.Len(t, visible, 2)
	assert.ElementsMatch(t, []string{"a", "d"}, []string{visible[0].HarpID, visible[1].HarpID})
	assert.Equal(t, 1, hiddenCompleted)
	assert.Equal(t, 1, hiddenDeferred)
}

func TestFilterActiveDefault_IncludeDoneBypassesFiltering(t *testing.T) {
	list := []tasks.Task{
		{HarpID: "a", Status: tasks.StatusToDo},
		{HarpID: "b", Status: tasks.StatusDone, Checked: true},
	}

	visible, hiddenCompleted, hiddenDeferred := filterActiveDefault(list, true, nil)
	assert.Len(t, visible, 2)
	assert.Zero(t, hiddenCompleted)
	assert.Zero(t, hiddenDeferred)
}

func TestFilterActiveDefault_ExplicitStatusBypassesFiltering(t *testing.T) {
	list := []tasks.Task{
		{HarpID: "b", Status: tasks.StatusDone, Checked: true},
	}

	visible, hiddenCompleted, hiddenDeferred := filterActiveDefault(list, false, []string{tasks.StatusDone})
	assert.Len(t, visible, 1, "an explicit status filter is itself the opt-in")
	assert.Zero(t, hiddenCompleted)
	assert.Zero(t, hiddenDeferred)
}

func TestListAllProjects_EmptyTasksDirIsNotAnError(t *testing.T) {
	taskstest.Isolate(t) // fresh HOME: ~/.ctxloom/tasks doesn't exist yet

	got, err := listAllProjects(nil, "", "", false, false, nil, time.Time{}, "", 0)
	require.NoError(t, err)
	assert.Zero(t, got.ProjectCount)
	assert.Empty(t, got.Rows)
}

func TestListAllProjects_AggregatesAcrossProjectFilesSortedDeterministically(t *testing.T) {
	taskstest.Isolate(t)

	_, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-b"}, "b's active task", "", "")
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "proj-b"}, "b's done task", tasks.StatusDone, "")
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "a's active task", "", "")
	require.NoError(t, err)

	got, err := listAllProjects(nil, "", "", false, false, nil, time.Time{}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 2, got.ProjectCount)
	assert.Equal(t, 1, got.HiddenCompleted, "the done task in proj-b is hidden by the default active-only view")

	require.Len(t, got.Rows, 2, "only the two active tasks are visible")
	assert.Equal(t, "proj-a", got.Rows[0].ProjectID, "rows sort by project-id first")
	assert.Equal(t, "proj-b", got.Rows[1].ProjectID)
	assert.Equal(t, "a's active task", got.Rows[0].Text)
	assert.Equal(t, "b's active task", got.Rows[1].Text)
}

// TestListAllProjects_LimitCapsRowsAndReportsOmitted pins that a
// global listing previously ignored `limit` outright (the parameter didn't
// even exist), always returning every row from every project with no cap
// and omitted_by_limit permanently 0 — exactly the case the MCP task_list
// tool's own instructions tell an agent to rely on `limit` for.
func TestListAllProjects_LimitCapsRowsAndReportsOmitted(t *testing.T) {
	taskstest.Isolate(t)

	_, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "task 1", "", "")
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "task 2", "", "")
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "proj-b"}, "task 3", "", "")
	require.NoError(t, err)

	got, err := listAllProjects(nil, "", "", false, false, nil, time.Time{}, "", 2)
	require.NoError(t, err)
	assert.Equal(t, 2, got.ProjectCount, "every project is still scanned for HiddenCompleted/HiddenDeferred accounting")
	assert.Len(t, got.Rows, 2, "limit caps the TOTAL row count across every project")
	assert.Equal(t, 1, got.OmittedByLimit)
}

func TestListAllProjects_IncludeDoneSurfacesHiddenTasksToo(t *testing.T) {
	taskstest.Isolate(t)

	_, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "done", tasks.StatusDone, "")
	require.NoError(t, err)

	got, err := listAllProjects(nil, "", "", true, false, nil, time.Time{}, "", 0)
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)
	assert.Zero(t, got.HiddenCompleted)
}

// TestGlobalScopeLimitationNote_GeneralWhenWorkDirNotRepoHomed covers the
// common case: an ordinary (home-homed, or unconfigured) project gets the
// general caveat naming the ~/.ctxloom/tasks-only scope, not a
// project-specific claim it can't back up.
func TestGlobalScopeLimitationNote_GeneralWhenWorkDirNotRepoHomed(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()

	got := globalScopeLimitationNote(dir, rootCmd.PersistentFlags(), "")
	assert.Equal(t, globalScopeGeneralLimitation, got)
}

// TestGlobalScopeLimitationNote_EmptyWorkDirFallsBackToGeneral covers the
// no-WorkDir case: there's nothing to resolve a homing mode against, so the
// general caveat is the only honest answer. taskContext no longer produces an
// empty WorkDir — a work root that will not resolve is fatal there now — so
// this guards the pure function's own contract for any future caller.
func TestGlobalScopeLimitationNote_EmptyWorkDirFallsBackToGeneral(t *testing.T) {
	got := globalScopeLimitationNote("", nil, "")
	assert.Equal(t, globalScopeGeneralLimitation, got)
}

// TestGlobalScopeLimitationNote_NamesRepoHomedWorkDirSpecifically is the unit
// test behind TestRunListCmd_GlobalNamesCurrentProjectWhenRepoHomed /
// TestHandleTaskList_GlobalNamesCurrentProjectWhenRepoHomed: a workDir whose
// own .taskloom/config.yaml declares `homing: repo` gets a note naming that
// exact project, not the generic caveat.
func TestGlobalScopeLimitationNote_NamesRepoHomedWorkDirSpecifically(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()
	writeConfigForTest(t, dir, "homing: repo\n")

	got := globalScopeLimitationNote(dir, rootCmd.PersistentFlags(), "")
	assert.Contains(t, got, dir)
	assert.Contains(t, got, "repo-homed")
	assert.NotEqual(t, globalScopeGeneralLimitation, got)
}

func TestListAllProjects_TermFilterAppliesPerProject(t *testing.T) {
	taskstest.Isolate(t)

	_, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "fix the parser", "", "")
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "proj-b"}, "write docs", "", "")
	require.NoError(t, err)

	got, err := listAllProjects(nil, "parser", "", false, false, nil, time.Time{}, "", 0)
	require.NoError(t, err)
	require.Len(t, got.Rows, 1)
	assert.Equal(t, "proj-a", got.Rows[0].ProjectID)
	assert.True(t, strings.Contains(got.Rows[0].Text, "parser"))
}

// TestResolveListScope_HonorsTheCallersBoundaryRatherThanReResolving pins
// that the working directory is resolved once, by taskContext, and
// its boundary half travels on the context; resolveListScope used to throw
// that away and call workdir.ResolveBoundary() a second time, so the scope
// decision was made against a fresh resolution of the PROCESS cwd rather than
// against the directory it had just been handed.
//
// The two are deliberately in conflict here: cwd is a real git repository (a
// boundary), while the caller reports the directory it actually resolved as
// no boundary at all. A second resolution answers "boundary, stay
// project-scoped"; honoring the argument answers "no project here, fall back
// to global and say why". Only the second is correct, because the caller is
// the one that knows which directory the store was opened from.
func TestResolveListScope_HonorsTheCallersBoundaryRatherThanReResolving(t *testing.T) {
	taskstest.Isolate(t)
	cwd := t.TempDir()
	gitInit(t, cwd)
	taskstest.ChangeDir(t, cwd)

	elsewhere := t.TempDir() // no git, no marker, no registry entry

	scope, err := resolveListScope(false, "", elsewhere, false)
	require.NoError(t, err)
	assert.True(t, scope.Global,
		"the caller reported no boundary; re-resolving cwd (a git repo) must not override it")
	require.NotEmpty(t, scope.Notice)
	assert.Contains(t, scope.Notice, elsewhere,
		"the fallback must name the directory the CALLER resolved, not the process cwd")
}

// TestTaskContext_CarriesTheBoundaryItResolved keeps the end-to-end half that
// resolveListScope no longer performs: the boundary bit resolveListScope now
// trusts must actually be true inside a real git repository and false in a
// bare directory, or the collapse above would silently send every listing
// global.
func TestTaskContext_CarriesTheBoundaryItResolved(t *testing.T) {
	t.Run("git repo is a boundary", func(t *testing.T) {
		taskstest.Isolate(t)
		dir := t.TempDir()
		gitInit(t, dir)
		taskstest.ChangeDir(t, dir)

		tc, err := taskContext()
		require.NoError(t, err)
		assert.True(t, tc.WorkDirIsBoundary)
		assert.NotEmpty(t, tc.WorkDir)
	})

	t.Run("bare directory is not", func(t *testing.T) {
		taskstest.Isolate(t)
		dir := t.TempDir()
		taskstest.ChangeDir(t, dir)

		tc, err := taskContext()
		require.NoError(t, err)
		assert.False(t, tc.WorkDirIsBoundary)
	})
}

// TestListAllProjects_ByPriority_RanksWithinEachProjectAndReportsWarnings
// characterizes the one arm of listAllProjects that nothing else covered: the
// byPriority branch, which snapshots each project separately, computes ranks
// against that project's OWN full population, attaches them, collects each
// project's degenerate-ranking diagnostic under a "project <id>:" prefix, and
// then reorders within — but never across — a project's group.
//
// It is deliberately asymmetric: proj-a declares the tag the priority_fn
// reads, proj-b does not, so the same schema produces a real ranking on one
// side and an all-tied one on the other. That is the only shape in which the
// per-project scoping, the per-project warning prefix, and the
// grouping-survives-the-sort rule are all observable at once.
func TestListAllProjects_ByPriority_RanksWithinEachProjectAndReportsWarnings(t *testing.T) {
	taskstest.Isolate(t)

	schema, err := tagschema.Parse([]string{
		`tagma.priority_fn:"triage:impact"="{{triage:impact}}"`,
	})
	require.NoError(t, err)

	a := operations.TaskContext{ProjectID: "proj-a", TagSchema: schema}
	_, err = operations.AddTaskWithTags(a, "a low", "", "", []string{"triage:impact=1"})
	require.NoError(t, err)
	_, err = operations.AddTaskWithTags(a, "a high", "", "", []string{"triage:impact=9"})
	require.NoError(t, err)

	b := operations.TaskContext{ProjectID: "proj-b", TagSchema: schema}
	_, err = operations.AddTask(b, "b untagged one", "", "")
	require.NoError(t, err)
	// AllTied needs more than one non-terminal task to mean anything.
	_, err = operations.AddTask(b, "b untagged two", "", "")
	require.NoError(t, err)

	// A SECOND degenerate project, so the joining of two projects' findings
	// is observable at all: with only one warning project every separator
	// looks alike.
	c := operations.TaskContext{ProjectID: "proj-c", TagSchema: schema}
	_, err = operations.AddTask(c, "c untagged one", "", "")
	require.NoError(t, err)
	_, err = operations.AddTask(c, "c untagged two", "", "")
	require.NoError(t, err)

	got, err := listAllProjects(nil, "", "", false, true, schema, time.Now(), "", 0)
	require.NoError(t, err)
	require.Len(t, got.Rows, 6)

	// Grouping survives the priority sort: every proj-a row precedes proj-b.
	assert.Equal(t, []string{"proj-a", "proj-a", "proj-b", "proj-b", "proj-c", "proj-c"},
		[]string{got.Rows[0].ProjectID, got.Rows[1].ProjectID, got.Rows[2].ProjectID,
			got.Rows[3].ProjectID, got.Rows[4].ProjectID, got.Rows[5].ProjectID})
	assert.Equal(t, "a high", got.Rows[0].Text, "highest impact ranks first inside its own project")
	assert.Equal(t, "a low", got.Rows[1].Text)

	for i, r := range got.Rows {
		require.NotNil(t, r.DerivedPriority, "row %d must carry a computed priority", i)
	}
	assert.Greater(t, priorityOf(got.Rows[0].Task), priorityOf(got.Rows[1].Task))

	assert.Contains(t, got.PriorityWarning, "project proj-b:",
		"a degenerate per-project ranking must be attributed to that project")
	assert.Contains(t, got.PriorityWarning, "project proj-c:")
	assert.NotContains(t, got.PriorityWarning, "project proj-a:",
		"a healthy ranking must stay quiet")

	// Every finding is one whole line owned by one project. Each project's
	// warning is itself multi-line (the tied ranking, then the uncovered
	// formula term), so running two of them together on one line produces a
	// sentence that reads as a single finding about both.
	lines := strings.Split(got.PriorityWarning, "\n")
	assert.Greater(t, len(lines), 2, "two degenerate projects, each reporting more than one finding")
	for _, line := range lines {
		assert.Regexp(t, `^project proj-[bc]: \S`, line)
		assert.Equal(t, 1, strings.Count(line, "project proj-"),
			"a line must name exactly one project, not run two findings together: %q", line)
	}
}

// A --global listing interleaves several projects' findings, and the
// coverage diagnostic is multi-line -- one line per thinly-covered formula
// term. Attributing only the first line would leave every line after it
// reading as a finding about whichever project happened to be named last,
// which is worse than no attribution: it is confident and wrong.
func TestAttributeToProject_EveryLineNamesItsProject(t *testing.T) {
	got := attributeToProject("high-tight-gulf", "first finding\nsecond finding\nthird finding")

	assert.Equal(t, []string{
		"project high-tight-gulf: first finding",
		"project high-tight-gulf: second finding",
		"project high-tight-gulf: third finding",
	}, strings.Split(got, "\n"))
}

// A project whose ranking is healthy contributes nothing. Returning a bare
// "project X: " would put an empty finding into the joined warning and make
// a clean project look like a reported one.
func TestAttributeToProject_HealthyProjectContributesNothing(t *testing.T) {
	assert.Empty(t, attributeToProject("high-tight-gulf", ""))
}
