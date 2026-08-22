package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
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
	return taskstest.ProjectDir(t)
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

// TestHandleTaskList_SortPriorityOrdersDescendingByLevel exercises
// task_list's sort="priority" against the project's DEFAULT tag_schema
// (taskloomconfig.DefaultTagSchema) end to end: three tasks differing ONLY
// in triage:level must come back worst-consequence (level=1) first, each
// carrying a populated derived_priority.
//
// Every other target either formula reads is applied identically to all
// three — which is what isolates triage:level as the sole cause of the
// ordering AND what keeps res.PriorityWarning empty: full per-target
// coverage is the healthy case the coverage diagnostic must stay quiet on.
func TestHandleTaskList_SortPriorityOrdersDescendingByLevel(t *testing.T) {
	withProjectDir(t)

	shared := []string{"triage:kind=defect", "triage:effort=1", "triage:blocks-release=0.7.0", "triage:exposed=cli", "triage:blind-gate=lint"}
	for _, tc := range []struct{ text, level string }{
		{"low consequence", "triage:level=5"},
		{"high consequence", "triage:level=1"},
		{"mid consequence", "triage:level=3"},
	} {
		_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: tc.text, Tags: append([]string{tc.level}, shared...)})
		require.NoError(t, err)
	}

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Sort: "priority"})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 3)
	assert.Equal(t, "high consequence", res.Tasks[0].Text)
	assert.Equal(t, "mid consequence", res.Tasks[1].Text)
	assert.Equal(t, "low consequence", res.Tasks[2].Text)
	for _, row := range res.Tasks {
		require.NotNil(t, row.DerivedPriority, "sort=priority must populate derived_priority")
	}
	assert.Greater(t, *res.Tasks[0].DerivedPriority, *res.Tasks[2].DerivedPriority)
	assert.Empty(t, res.PriorityWarning, "a real spread of levels, every formula term covered, is a meaningful ranking")
}

// TestHandleTaskList_CompactReturnsCompactRowsAndDefaultsStayFull pins the
// additive/backward-compat contract: task_list with compact=true returns
// compact rows (harp_id, status, checked, tags, headline -- no full text)
// in compact_tasks, while leaving Tasks nil; the DEFAULT call (compact
// omitted, exactly today's shape) still returns full records in Tasks with
// compact_tasks absent.
func TestHandleTaskList_CompactReturnsCompactRowsAndDefaultsStayFull(t *testing.T) {
	withProjectDir(t)

	longText := "fix the widget\nprovenance: found in session swift-amber-falcon on 2026-07-24"
	_, add, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: longText, Tags: []string{"urgent"}})
	require.NoError(t, err)

	// Backward-compat: omitting compact preserves today's exact full-record
	// shape.
	_, full, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)
	require.Len(t, full.Tasks, 1)
	assert.Equal(t, longText, full.Tasks[0].Text, "default (compact unset) must still return the full task body")
	assert.Empty(t, full.CompactTasks, "default (compact unset) must not populate compact_tasks")

	// compact=true: compact_tasks is populated instead, and never carries the
	// full text.
	_, compact, err := handleTaskList(context.Background(), nil, taskListInput{Compact: true})
	require.NoError(t, err)
	assert.Empty(t, compact.Tasks, "compact=true must not also populate the full tasks field")
	require.Len(t, compact.CompactTasks, 1)
	row := compact.CompactTasks[0]
	assert.Equal(t, add.Task.HarpID, row.HarpID)
	assert.Equal(t, "To Do", row.Status)
	assert.False(t, row.Checked)
	assert.Equal(t, []string{"urgent"}, row.Tags)
	assert.Equal(t, "fix the widget", row.Headline, "headline is the first line only")

	b, err := json.Marshal(compact.CompactTasks)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "provenance", "compact rows must never carry the full text")
	assert.NotContains(t, string(b), `"trigger"`)
	assert.NotContains(t, string(b), `"text_hash"`)
}

// TestHandleTaskList_LimitCapsRowsWithoutAffectingSummary pins limit's
// contract at the MCP surface: rows are capped, omitted_by_limit reports how
// many, and include_summary's counts stay the FULL uncapped counts.
func TestHandleTaskList_LimitCapsRowsWithoutAffectingSummary(t *testing.T) {
	withProjectDir(t)

	for i := 0; i < 5; i++ {
		_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: fmt.Sprintf("task %d", i)})
		require.NoError(t, err)
	}

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Limit: 2, IncludeSummary: true})
	require.NoError(t, err)
	assert.Len(t, res.Tasks, 2)
	assert.Equal(t, 3, res.OmittedByLimit)
	require.NotNil(t, res.Summary)
	assert.Equal(t, 5, res.Summary.Counts["To Do"], "include_summary must cover every task, not just the capped page")

	// limit=0 (default/omitted) is today's exact unlimited behavior.
	_, unlimited, err := handleTaskList(context.Background(), nil, taskListInput{})
	require.NoError(t, err)
	assert.Len(t, unlimited.Tasks, 5)
	assert.Zero(t, unlimited.OmittedByLimit)
}

// TestHandleTaskList_UnknownSortValueErrors mirrors the CLI's --sort
// validation: task_list's sort input has the same closed value set.
func TestHandleTaskList_UnknownSortValueErrors(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskList(context.Background(), nil, taskListInput{Sort: "bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown sort value")
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
	// The agent typing tag_query is the caller MOST likely to get the postfix
	// grammar wrong, and the only one who cannot read `taskloom list --help`.
	assert.Contains(t, err.Error(), "queries are postfix",
		"the MCP tool must carry the same grammar hint the CLI gives, not tagma's bare stack error")
}

// TestHandleTaskList_MalformedTagQueryErrorMatchesTheCLI pins the two list
// surfaces to one diagnostic for one mistake. They resolve, filter and project
// through the same pipeline; the error a malformed query produces must not
// depend on which surface asked.
func TestHandleTaskList_MalformedTagQueryErrorMatchesTheCLI(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "a task"})
	require.NoError(t, err)

	_, _, mcpErr := handleTaskList(context.Background(), nil, taskListInput{TagQuery: "and"})
	require.Error(t, mcpErr)

	tc, err := taskContext()
	require.NoError(t, err)
	tc, err = resolveHoming(tc)
	require.NoError(t, err)
	_, cliErr := operations.ListTasks(tc, operations.ListOptions{TagQuery: "and"})
	require.Error(t, cliErr)

	assert.Equal(t, wrapTagQueryError(cliErr).Error(), mcpErr.Error())
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
// with the project they came from. Notice is NEVER empty when Global is true
// (see smart-veal/globalScopeLimitationNote): even an explicit opt-in must
// declare that repo-homed stores are outside what this aggregation can see.
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
	require.NotEmpty(t, res.Notice, "the scope limitation must always be disclosed for a global listing")
	assert.Contains(t, res.Notice, "repo-homed")

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

// TestHandleTaskList_GlobalNamesCurrentProjectWhenRepoHomed sharpens the
// scope notice (mirrors TestRunListCmd_GlobalNamesCurrentProjectWhenRepoHomed
// on the CLI side): when the CURRENT project is itself repo-homed, the
// notice returned to an MCP caller must name it specifically -- the case
// most likely to bite, since the agent is standing inside the very project
// that would otherwise vanish from an "everything" listing with no trace.
func TestHandleTaskList_GlobalNamesCurrentProjectWhenRepoHomed(t *testing.T) {
	proj := withProjectDir(t)
	writeConfigForTest(t, proj, "homing: repo\n")

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Global: true})
	require.NoError(t, err)
	assert.True(t, res.Global)
	require.NotEmpty(t, res.Notice)
	assert.Contains(t, res.Notice, proj, "the notice must name the excluded project's own path")
	assert.Contains(t, res.Notice, "repo-homed")
}

// mcpWarningContext puts a project-resolution warning in play for a MUTATION:
// a pinned --project/CTXLOOM_PROJECT_ID is a harmless no-op in repo-homed
// mode (the repo path is the identity), and operations reports that rather
// than swallowing it. Returns the resolved project dir.
func mcpWarningContext(t *testing.T) string {
	t.Helper()
	dir := taskstest.ProjectDir(t)
	t.Setenv("CTXLOOM_PROJECT_ID", "pinned-but-irrelevant")
	t.Setenv("TASKLOOM_CONFIG_HOMING", "repo")
	return dir
}

// Every MCP WRITE tool dropped the project-resolution warning and the store
// attribution on the floor: warnTask writes to the server's STDERR, which the
// model driving these tools never sees, and the returned result carried only
// path+task. So an agent whose write landed in a moved/forked store — or
// whose pinned project id was ignored outright — was told, in the only
// channel it can read, that everything was fine.
func TestMCPWriteTools_SurfaceTheWarningAndTheStoreTheWriteLandedIn(t *testing.T) {
	t.Run("task_add", func(t *testing.T) {
		mcpWarningContext(t)
		_, res, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "ship it"})
		require.NoError(t, err)
		assert.Contains(t, res.Warning, "pinned-but-irrelevant",
			"the caller must be told its pinned project id was ignored")
		assert.NotEmpty(t, res.ProjectDir, "the model must be able to see WHICH store it wrote to")
	})

	t.Run("task_set_status", func(t *testing.T) {
		mcpWarningContext(t)
		_, added, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "ship it"})
		require.NoError(t, err)
		_, res, err := handleTaskSetStatus(context.Background(), nil,
			taskSetStatusInput{HarpID: added.Task.HarpID, Status: tasks.StatusDone})
		require.NoError(t, err)
		assert.Contains(t, res.Warning, "pinned-but-irrelevant")
		assert.NotEmpty(t, res.ProjectDir)
	})

	t.Run("task_edit", func(t *testing.T) {
		mcpWarningContext(t)
		_, added, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "ship it"})
		require.NoError(t, err)
		_, res, err := handleTaskEdit(context.Background(), nil,
			taskEditInput{HarpID: added.Task.HarpID, Text: "ship it harder"})
		require.NoError(t, err)
		assert.Contains(t, res.Warning, "pinned-but-irrelevant")
		assert.NotEmpty(t, res.ProjectDir)
	})

	t.Run("task_tag", func(t *testing.T) {
		mcpWarningContext(t)
		_, added, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "ship it"})
		require.NoError(t, err)
		_, res, err := handleTaskTag(context.Background(), nil,
			taskTagInput{HarpID: added.Task.HarpID, Add: []string{"urgent"}})
		require.NoError(t, err)
		assert.Contains(t, res.Warning, "pinned-but-irrelevant")
		assert.NotEmpty(t, res.ProjectDir)
	})
}
