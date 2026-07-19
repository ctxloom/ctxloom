package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestNoteHiddenMatches pins the anti-silent-truncation hint: a --term or
// --tag-query listing whose matches were partly suppressed by the default
// active-only view says so (with per-kind counts and the flag that reveals
// them), while an unfiltered listing or one with nothing hidden stays quiet.
func TestNoteHiddenMatches(t *testing.T) {
	cases := []struct {
		name      string
		res       operations.TaskListResult
		filtered  bool
		wantParts []string // all must appear; empty = no output at all
	}{
		{
			name:      "filtered with completed and deferred hidden",
			res:       operations.TaskListResult{HiddenCompleted: 37, HiddenDeferred: 1},
			filtered:  true,
			wantParts: []string{"38 more matching task(s)", "37 completed", "1 deferred", "--all"},
		},
		{
			name:      "filtered with only completed hidden omits the deferred clause",
			res:       operations.TaskListResult{HiddenCompleted: 2},
			filtered:  true,
			wantParts: []string{"2 more matching task(s)", "2 completed", "--all"},
		},
		{
			name:     "unfiltered listing stays quiet even with hidden tasks",
			res:      operations.TaskListResult{HiddenCompleted: 40, HiddenDeferred: 3},
			filtered: false,
		},
		{
			name:     "filtered with nothing hidden stays quiet",
			res:      operations.TaskListResult{},
			filtered: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf strings.Builder
			noteHiddenMatches(&buf, &c.res, c.filtered)
			out := buf.String()
			if len(c.wantParts) == 0 {
				if out != "" {
					t.Fatalf("expected no hint, got %q", out)
				}
				return
			}
			for _, part := range c.wantParts {
				if !strings.Contains(out, part) {
					t.Fatalf("hint %q missing %q", out, part)
				}
			}
			if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
				t.Fatalf("hint must be exactly one line, got %q", out)
			}
			if c.res.HiddenDeferred == 0 && strings.Contains(out, "deferred") {
				t.Fatalf("zero deferred must not be mentioned: %q", out)
			}
			if c.res.HiddenCompleted == 0 && strings.Contains(out, "completed") {
				t.Fatalf("zero completed must not be mentioned: %q", out)
			}
		})
	}
}

// TestRunListCmd_DefaultScopesToCurrentProjectOnly is the CLI-side mirror of
// TestHandleTaskList_DefaultScopesToCurrentProjectOnly: `taskloom list` with
// no flags shows only the tasks of the project resolved from the working
// directory.
func TestRunListCmd_DefaultScopesToCurrentProjectOnly(t *testing.T) {
	taskstest.ProjectDir(t)

	_, err := operations.AddTaskWithTags(taskContext(), "here's task", "", "", nil)
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, taskContext(), listOptions{Format: clifmt.FormatText})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "here's task")
	assert.NotContains(t, stdout.String(), "elsewhere's task")
	assert.Empty(t, stderr.String(), "a resolved project needs no notice")
}

// TestRunListCmd_GlobalAggregatesAcrossProjects is the CLI-side mirror of
// TestHandleTaskList_GlobalAggregatesAcrossProjects: `taskloom list --global`
// shows every project's tasks, grouped by project, with no notice (an
// explicit opt-in needs no explaining).
func TestRunListCmd_GlobalAggregatesAcrossProjects(t *testing.T) {
	taskstest.ProjectDir(t)

	_, err := operations.AddTaskWithTags(taskContext(), "here's task", "", "", nil)
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, taskContext(), listOptions{Global: true, Format: clifmt.FormatText})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "Projects: 2 (--global)")
	assert.Contains(t, stdout.String(), "here's task")
	assert.Contains(t, stdout.String(), "elsewhere's task")
	assert.Contains(t, stdout.String(), "Project: elsewhere")
	assert.Empty(t, stderr.String(), "an explicit --global needs no notice")
}

// TestRunListCmd_NoProjectContextDefaultsGlobalWithNotice is the CLI-side
// mirror of TestHandleTaskList_NoProjectContextDefaultsGlobalWithNotice: run
// from a directory that is neither a git repo nor an already-established
// project, `taskloom list` (no flags) falls back to --global and explains why
// on stderr.
func TestRunListCmd_NoProjectContextDefaultsGlobalWithNotice(t *testing.T) {
	taskstest.Isolate(t)
	dir := t.TempDir()
	taskstest.ChangeDir(t, dir)

	_, err := operations.AddTask(operations.TaskContext{ProjectID: "somewhere"}, "a tracked task", "", "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, taskContext(), listOptions{Format: clifmt.FormatText})
	require.NoError(t, err)

	assert.Contains(t, stderr.String(), "no project detected", "the fallback must be explained, not silent")
	assert.Contains(t, stdout.String(), "a tracked task")
	assert.Contains(t, stdout.String(), "Projects: 1 (--global)")
}

// TestRunListCmd_JSONOutputTagsEachRowWithItsProject exercises the --json
// path of a --global listing: it must emit taskRow (task + project_id), not
// a bare tasks.Task, so a scripted consumer can tell which project each task
// came from.
func TestRunListCmd_JSONOutputTagsEachRowWithItsProject(t *testing.T) {
	taskstest.Isolate(t)
	_, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "a's task", "", "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, operations.TaskContext{}, listOptions{Global: true, Format: clifmt.FormatJSON})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), `"project_id": "proj-a"`)
	assert.Contains(t, stdout.String(), `"text": "a's task"`)
}

// TestRenderTaskTable_SummarizesAndSeparates pins the human `list` view: entries
// are blank-line separated so they don't run together into a wall, and long
// text collapses to a one-line summary by default while full restores it.
func TestRenderTaskTable_SummarizesAndSeparates(t *testing.T) {
	long := strings.Repeat("x", 200)
	list := []tasks.Task{
		{HarpID: "aaa-bbb", Status: "To Do", Text: long},
		{HarpID: "ccc-ddd", Status: "Done", Checked: true, Text: "short"},
	}

	var b strings.Builder
	require.NoError(t, renderTaskTable(&b, list))
	out := b.String()
	assert.Contains(t, out, "\n\n", "entries must be separated by a blank line, not run together")
	assert.NotContains(t, out, long, "the list view summarizes rather than printing full text")
	assert.Contains(t, out, "…", "a truncated summary ends with an ellipsis")
}

// TestRenderTaskDetail_ShowsFullTextAndMetadata pins `taskloom show`: it prints
// the complete (untruncated) text plus status, tags, and trigger — the detail
// the summarized list view leaves out.
func TestRenderTaskDetail_ShowsFullTextAndMetadata(t *testing.T) {
	long := strings.Repeat("y", 200)
	task := tasks.Task{
		HarpID:  "aaa-bbb",
		Status:  "Deferred",
		Text:    long,
		Tags:    []string{"alpha", "beta"},
		Trigger: "the v2 API ships",
	}

	var b strings.Builder
	require.NoError(t, renderTaskDetail(&b, task))
	out := b.String()
	assert.Contains(t, out, long, "show prints the complete text, untruncated")
	assert.NotContains(t, out, "…", "show never summarizes")
	assert.Contains(t, out, "aaa-bbb")
	assert.Contains(t, out, "Deferred")
	assert.Contains(t, out, "tags: alpha, beta")
	assert.Contains(t, out, "trigger: the v2 API ships")
}
