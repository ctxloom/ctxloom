package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// seedTwoProjects puts one In Progress and one To Do task in the current
// project and one In Progress and one Done task in "elsewhere", all under the
// temp HOME taskstest.ProjectDir roots. It returns the two In Progress harps.
func seedTwoProjects(t *testing.T) (here, elsewhere string) {
	t.Helper()
	taskstest.ProjectDir(t)
	tc := mustTaskContext(t)
	other := operations.TaskContext{ProjectID: "elsewhere"}

	res, err := operations.AddTask(tc, "here in flight", tasks.StatusInProgress, "")
	require.NoError(t, err)
	here = res.Task.HarpID
	_, err = operations.AddTask(tc, "here queued", "", "")
	require.NoError(t, err)
	res, err = operations.AddTask(other, "elsewhere in flight", tasks.StatusInProgress, "")
	require.NoError(t, err)
	elsewhere = res.Task.HarpID
	_, err = operations.AddTask(other, "elsewhere finished", tasks.StatusDone, "")
	require.NoError(t, err)
	return here, elsewhere
}

// A harp id is unique only within its project, so a cross-project summary
// names each in-progress task WITH its project; the counts are summed.
func TestRunSummaryCmd_GlobalSumsCountsAndAttributesInProgress(t *testing.T) {
	_, elsewhere := seedTwoProjects(t)

	var stdout, stderr strings.Builder
	require.NoError(t, runSummaryCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Global: true, Format: clifmt.FormatText}))

	out := stdout.String()
	assert.Contains(t, out, "Projects: 2 (--global)")
	assert.Contains(t, out, tasks.StatusInProgress+"\t2", "in-progress counts are summed across projects")
	assert.Contains(t, out, tasks.StatusToDo+"\t1")
	assert.Contains(t, out, tasks.StatusDone+"\t1", "completed tasks are counted: a summary covers every task")
	assert.Contains(t, out, elsewhere+" (elsewhere)", "each in-progress harp names its project")
	assert.Contains(t, stderr.String(), "repo-homed", "a global read declares the stores it cannot see")
}

func TestRunSummaryCmd_InProgressJSONCarriesProject(t *testing.T) {
	here, elsewhere := seedTwoProjects(t)

	var stdout, stderr strings.Builder
	require.NoError(t, runSummaryCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Global: true, Format: clifmt.FormatJSON}))

	var got struct {
		Counts     map[string]int `json:"counts"`
		InProgress []struct {
			HarpID  string `json:"harp_id"`
			Project string `json:"project"`
		} `json:"in_progress"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout.String()), &got), stdout.String())
	assert.Equal(t, 2, got.Counts[tasks.StatusInProgress])
	require.Len(t, got.InProgress, 2)
	projects := map[string]string{}
	for _, ip := range got.InProgress {
		projects[ip.HarpID] = ip.Project
	}
	assert.Equal(t, "elsewhere", projects[elsewhere])
	assert.NotEmpty(t, projects[here], "the current project's in-progress task is attributed too")
}

func TestRunSummaryCmd_SingleProjectCountsOnlyItsOwnStore(t *testing.T) {
	here, elsewhere := seedTwoProjects(t)

	var stdout, stderr strings.Builder
	require.NoError(t, runSummaryCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Format: clifmt.FormatText}))

	out := stdout.String()
	assert.Contains(t, out, tasks.StatusInProgress+"\t1")
	assert.NotContains(t, out, tasks.StatusDone, "elsewhere's Done task is not this project's")
	assert.Contains(t, out, "In-progress: "+here)
	assert.NotContains(t, out, elsewhere)
}

// task_list's include_summary returns the same shape as `summary --global`
// when the listing is global, and covers every task, not the filtered page.
func TestHandleTaskList_GlobalIncludeSummary(t *testing.T) {
	_, elsewhere := seedTwoProjects(t)

	_, res, err := handleTaskList(context.Background(), nil, taskListInput{Global: true, IncludeSummary: true, Term: "queued"})
	require.NoError(t, err)
	require.NotNil(t, res.Summary, "a global listing carries the summary it was asked for")
	assert.Equal(t, 2, res.Summary.Counts[tasks.StatusInProgress], "the summary ignores the listing's filters")
	assert.Contains(t, res.Summary.InProgress, tasks.InProgressTask{HarpID: elsewhere, Project: "elsewhere"})
}
