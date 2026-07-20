package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// withProjectDir isolates the per-project task log for the duration of the test.
// The log is home-rooted (~/.ctxloom/tasks/<project-id>.jsonl) and the project-id
// comes from the CTXLOOM_PROJECT_ID env the host ctxloom process exports, so a
// run inside a real session would otherwise resolve to that project's live log
// and leak ~hundreds of tasks into these assertions. taskstest.ProjectDir roots
// HOME at a fresh tempdir, clears the session/project env so resolution mints a
// tempdir-scoped project-id, and switches the working directory. Nothing touches
// the real ~/.ctxloom.
func withProjectDir(t *testing.T) string {
	t.Helper()
	dir := taskstest.ProjectDir(t)
	setTestHoming(t, "home")
	return dir
}

func TestHandleTaskAdd_AssignsHarpIDAndPersists(t *testing.T) {
	withProjectDir(t)

	_, res, err := handleTaskAdd(context.Background(), nil, taskAddInput{
		Text: "write the storage layer",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEmpty(t, res.Task.HarpID, "Add should assign a harp ID")
	assert.Equal(t, "write the storage layer", res.Task.Text)
	assert.Equal(t, "To Do", res.Task.Status, "empty input status should default to To Do")
}

func TestHandleTaskList_FiltersByStatusAndTerm(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "alpha", Status: "In Progress"})
	require.NoError(t, err)
	_, _, err = handleTaskAdd(context.Background(), nil, taskAddInput{Text: "beta", Status: "To Do"})
	require.NoError(t, err)
	_, _, err = handleTaskAdd(context.Background(), nil, taskAddInput{Text: "gamma", Status: "Done"})
	require.NoError(t, err)

	// Status filter
	_, res, err := handleTaskList(context.Background(), nil, taskListInput{
		Statuses: []string{"In Progress", "To Do"},
	})
	require.NoError(t, err)
	assert.Len(t, res.Tasks, 2)

	// Term filter (case-insensitive); a completed task needs include_completed.
	_, res, err = handleTaskList(context.Background(), nil, taskListInput{Term: "GAMMA"})
	require.NoError(t, err)
	assert.Empty(t, res.Tasks, "completed task is hidden from the default term search")

	_, res, err = handleTaskList(context.Background(), nil, taskListInput{Term: "GAMMA", IncludeCompleted: true})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, "gamma", res.Tasks[0].Text)

	// include_summary attaches counts + in-progress IDs
	_, res, err = handleTaskList(context.Background(), nil, taskListInput{IncludeSummary: true})
	require.NoError(t, err)
	require.NotNil(t, res.Summary)
	assert.Equal(t, 1, res.Summary.Counts["In Progress"])
	assert.Len(t, res.Summary.InProgress, 1)
}

func TestHandleTaskSetStatus_MovesTask(t *testing.T) {
	withProjectDir(t)

	_, add, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "do the thing"})
	require.NoError(t, err)
	harpID := add.Task.HarpID

	_, set, err := handleTaskSetStatus(context.Background(), nil, taskSetStatusInput{
		HarpID: harpID,
		Status: "Done",
	})
	require.NoError(t, err)
	assert.Equal(t, "Done", set.Task.Status)
	assert.True(t, set.Task.Checked)

	// Surfaced via list as well.
	_, list, err := handleTaskList(context.Background(), nil, taskListInput{Statuses: []string{"Done"}})
	require.NoError(t, err)
	require.Len(t, list.Tasks, 1)
	assert.Equal(t, harpID, list.Tasks[0].HarpID)
}

func TestHandleTaskSetStatus_UnknownHarpIDErrors(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskSetStatus(context.Background(), nil, taskSetStatusInput{
		HarpID: "no-such-id",
		Status: "Done",
	})
	assert.Error(t, err)
}

func TestHandleTaskEdit_ReplacesText(t *testing.T) {
	withProjectDir(t)

	_, add, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "old text"})
	require.NoError(t, err)

	_, edit, err := handleTaskEdit(context.Background(), nil, taskEditInput{
		HarpID: add.Task.HarpID,
		Text:   "new text",
	})
	require.NoError(t, err)
	assert.Equal(t, "new text", edit.Task.Text)
	assert.Equal(t, add.Task.HarpID, edit.Task.HarpID, "identity is stable across an edit")
}

func TestHandleTaskAdd_SetsInitialTags(t *testing.T) {
	withProjectDir(t)

	_, res, err := handleTaskAdd(context.Background(), nil, taskAddInput{
		Text: "write the storage layer",
		Tags: []string{"beta", "alpha", "beta"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, res.Task.Tags, "tags come back sorted+deduped")
}

func TestHandleTaskTag_AddsAndRemoves(t *testing.T) {
	withProjectDir(t)

	_, add, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "ship it"})
	require.NoError(t, err)

	_, tagged, err := handleTaskTag(context.Background(), nil, taskTagInput{
		HarpID: add.Task.HarpID,
		Add:    []string{"urgent", "release"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"release", "urgent"}, tagged.Task.Tags)

	_, untagged, err := handleTaskTag(context.Background(), nil, taskTagInput{
		HarpID: add.Task.HarpID,
		Remove: []string{"urgent"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"release"}, untagged.Task.Tags)
}

func TestHandleTaskTag_UnknownHarpIDErrors(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskTag(context.Background(), nil, taskTagInput{
		HarpID: "no-such-id",
		Add:    []string{"urgent"},
	})
	assert.Error(t, err)
}

func TestHandleTaskTag_RequiresAddOrRemove(t *testing.T) {
	withProjectDir(t)

	_, add, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "ship it"})
	require.NoError(t, err)

	_, _, err = handleTaskTag(context.Background(), nil, taskTagInput{HarpID: add.Task.HarpID})
	assert.Error(t, err, "task_tag with neither add nor remove must not silently no-op")
}

func TestHandleTaskList_FiltersByTagQuery(t *testing.T) {
	withProjectDir(t)

	_, urgentRelease, err := handleTaskAdd(context.Background(), nil, taskAddInput{
		Text: "urgent release work",
		Tags: []string{"urgent", "release"},
	})
	require.NoError(t, err)
	_, _, err = handleTaskAdd(context.Background(), nil, taskAddInput{Text: "just urgent", Tags: []string{"urgent"}})
	require.NoError(t, err)
	_, _, err = handleTaskAdd(context.Background(), nil, taskAddInput{Text: "no tags"})
	require.NoError(t, err)

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{TagQuery: "urgent/release/and"})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, urgentRelease.Task.HarpID, res.Tasks[0].HarpID)

	_, res, err = handleTaskList(context.Background(), nil, taskListInput{TagQuery: "urgent"})
	require.NoError(t, err)
	assert.Len(t, res.Tasks, 2)
}

func TestHandleTaskList_MalformedTagQueryErrors(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "a task"})
	require.NoError(t, err)

	_, _, err = handleTaskList(context.Background(), nil, taskListInput{TagQuery: "and"})
	assert.Error(t, err, "a malformed tag query must fail loud, never silently return an empty/all result")
}

// TestHandleTaskList_DefaultScopesToCurrentProjectOnly pins the headline
// behavior: without global=true, task_list only ever sees the tasks of the
// project resolved from the working directory, even when other projects have
// tasks of their own.
func TestHandleTaskList_DefaultScopesToCurrentProjectOnly(t *testing.T) {
	withProjectDir(t) // establishes and cds into a fresh project (call it "here")

	_, here, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "here's task"})
	require.NoError(t, err)

	// A second project's task, seeded directly by project-id — never touches
	// cwd, so it must not leak into a default (non-global) listing.
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "")
	require.NoError(t, err)

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, here.Task.HarpID, res.Tasks[0].HarpID)
	assert.False(t, res.Global)
	assert.Empty(t, res.Notice)
}

// TestHandleTaskList_GlobalAggregatesAcrossProjects pins the --global /
// all_projects opt-in: every project's tasks come back in one result, tagged
// with the project they came from, and no notice is attached (an explicit
// opt-in needs no explaining).
func TestHandleTaskList_GlobalAggregatesAcrossProjects(t *testing.T) {
	withProjectDir(t)

	_, here, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "here's task"})
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "")
	require.NoError(t, err)

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Global: true})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 2)
	assert.True(t, res.Global)
	assert.Equal(t, 2, res.ProjectCount)
	assert.Empty(t, res.Notice)

	byHarp := map[string]taskRow{}
	for _, row := range res.Tasks {
		byHarp[row.HarpID] = row
	}
	require.Contains(t, byHarp, here.Task.HarpID)
	hereRow := byHarp[here.Task.HarpID]
	assert.NotEmpty(t, hereRow.ProjectID, "each row is tagged with the project it came from")
	assert.NotEqual(t, "elsewhere", hereRow.ProjectID, "withProjectDir's project must not be named \"elsewhere\"")

	var sawElsewhere bool
	for _, row := range res.Tasks {
		if row.ProjectID == "elsewhere" {
			sawElsewhere = true
			assert.Equal(t, "elsewhere's task", row.Text)
		}
	}
	assert.True(t, sawElsewhere, "the seeded project's task must be present too")
}

// TestHandleTaskList_NoProjectContextDefaultsGlobalWithNotice pins the third
// required behavior: called from a directory that is neither a git repo nor
// an already-established project, task_list defaults to global on its own —
// and says so via Notice, rather than silently minting a throwaway project
// identity for an arbitrary directory or silently returning an empty list.
func TestHandleTaskList_NoProjectContextDefaultsGlobalWithNotice(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir() // no git, no marker, no registry entry
	taskstest.ChangeDir(t, dir)

	_, err := operations.AddTask(operations.TaskContext{ProjectID: "somewhere"}, "a tracked task", "", "")
	require.NoError(t, err)

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)
	assert.True(t, res.Global)
	require.NotEmpty(t, res.Notice, "the no-project fallback must be explained in the tool result, not silent")
	assert.Contains(t, res.Notice, "no project detected")
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, "somewhere", res.Tasks[0].ProjectID)
}
