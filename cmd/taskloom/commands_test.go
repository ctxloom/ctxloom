package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// writeConfigForTest writes body as dir's project .taskloom/config.yaml
// (taskloomconfig.DirName/FileName), creating the directory as needed —
// shared by any test that needs a real on-disk homing/tag-schema config
// rather than TaskContext's own fields set directly.
func writeConfigForTest(t *testing.T, dir, body string) {
	t.Helper()
	full := filepath.Join(dir, taskloomconfig.DirName, taskloomconfig.FileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

// mustTaskContext is taskContext(), failing the test immediately on error —
// the taskloom-wide worktree redirect (workdir.ResolveBoundary) can now fail
// loud on a stale linked-worktree pointer, and every existing test call site
// here expects a clean resolution.
func mustTaskContext(t *testing.T) operations.TaskContext {
	t.Helper()
	tc, err := taskContext()
	require.NoError(t, err)
	return tc
}

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

	_, err := operations.AddTaskWithTags(mustTaskContext(t), "here's task", "", "", nil)
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Format: clifmt.FormatText})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "here's task")
	assert.NotContains(t, stdout.String(), "elsewhere's task")
	assert.Empty(t, stderr.String(), "a resolved project needs no notice")
}

// TestRunListCmd_GlobalAggregatesAcrossProjects is the CLI-side mirror of
// TestHandleTaskList_GlobalAggregatesAcrossProjects: `taskloom list --global`
// shows every project's tasks, grouped by project, but ALWAYS carries the
// scope-limitation notice on stderr (see smart-veal/globalScopeLimitationNote)
// — an aggregation that silently under-reports repo-homed stores is worse
// than one that declares its scope, even for an explicit opt-in.
func TestRunListCmd_GlobalAggregatesAcrossProjects(t *testing.T) {
	taskstest.ProjectDir(t)

	_, err := operations.AddTaskWithTags(mustTaskContext(t), "here's task", "", "", nil)
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Global: true, Format: clifmt.FormatText})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "Projects: 2 (--global)")
	assert.Contains(t, stdout.String(), "here's task")
	assert.Contains(t, stdout.String(), "elsewhere's task")
	assert.Contains(t, stdout.String(), "Project: elsewhere")
	assert.Contains(t, stderr.String(), "repo-homed", "even an explicit --global must declare the stores it cannot see")
}

// TestRunListCmd_GlobalNamesCurrentProjectWhenRepoHomed sharpens the scope
// notice: when the CURRENT project (the one taskContext() resolved from cwd)
// is itself repo-homed, --global's notice must name it specifically — this
// is the case most likely to bite: the user is standing inside the very
// project that will be silently excluded from an "everything" listing.
func TestRunListCmd_GlobalNamesCurrentProjectWhenRepoHomed(t *testing.T) {
	proj := taskstest.ProjectDir(t)
	writeConfigForTest(t, proj, "homing: repo\n")

	var stdout, stderr strings.Builder
	err := runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Global: true, Format: clifmt.FormatText})
	require.NoError(t, err)

	assert.Contains(t, stderr.String(), proj, "the notice must name the excluded project's own path")
	assert.Contains(t, stderr.String(), "repo-homed")
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
	err = runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Format: clifmt.FormatText})
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

// TestRunListCmd_SortPriorityOrdersDescendingByImpact exercises `taskloom
// list --sort priority` end to end against a real store: three tasks with
// different triage:impact values must render highest-impact first, and each
// rendered task's derived priority must actually be populated and ordered.
func TestRunListCmd_SortPriorityOrdersDescendingByImpact(t *testing.T) {
	taskstest.ProjectDir(t)

	tc := mustTaskContext(t)
	schema, err := tagschema.Parse([]string{
		`tagma.priority_fn:"triage:impact"="{{triage:impact}}"`,
	})
	require.NoError(t, err)
	tc.TagSchema = schema

	_, err = operations.AddTaskWithTags(tc, "low impact", "", "", []string{"triage:impact=1"})
	require.NoError(t, err)
	_, err = operations.AddTaskWithTags(tc, "high impact", "", "", []string{"triage:impact=9"})
	require.NoError(t, err)
	_, err = operations.AddTaskWithTags(tc, "mid impact", "", "", []string{"triage:impact=5"})
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, tc, listOptions{Sort: sortPriority, Format: clifmt.FormatText})
	require.NoError(t, err)

	out := stdout.String()
	hi := strings.Index(out, "high impact")
	mid := strings.Index(out, "mid impact")
	lo := strings.Index(out, "low impact")
	require.True(t, hi >= 0 && mid >= 0 && lo >= 0, "all three tasks must appear: %q", out)
	assert.Less(t, hi, mid, "highest impact renders before mid")
	assert.Less(t, mid, lo, "mid impact renders before lowest")
}

// TestRunListCmd_UnknownSortValueErrors pins --sort's closed value set: only
// "priority" (sortPriority) is recognized; anything else is a loud error,
// never a silent no-op sort.
func TestRunListCmd_UnknownSortValueErrors(t *testing.T) {
	taskstest.ProjectDir(t)

	var stdout, stderr strings.Builder
	err := runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Sort: "bogus", Format: clifmt.FormatText})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown --sort value")
}

// TestRunListCmd_DefaultSortLeavesOrderUnchanged pins the additive-only
// promise: omitting --sort must leave today's add-order behavior exactly as
// it was before this feature existed.
func TestRunListCmd_DefaultSortLeavesOrderUnchanged(t *testing.T) {
	taskstest.ProjectDir(t)

	tc := mustTaskContext(t)
	_, err := operations.AddTaskWithTags(tc, "first added", "", "", nil)
	require.NoError(t, err)
	_, err = operations.AddTaskWithTags(tc, "second added", "", "", nil)
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, tc, listOptions{Format: clifmt.FormatText})
	require.NoError(t, err)

	out := stdout.String()
	assert.Less(t, strings.Index(out, "first added"), strings.Index(out, "second added"))
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
	require.NoError(t, renderTaskTable(&b, list, tagma.HideConfig{}))
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
	require.NoError(t, renderTaskDetail(&b, task, tagma.HideConfig{}))
	out := b.String()
	assert.Contains(t, out, long, "show prints the complete text, untruncated")
	assert.NotContains(t, out, "…", "show never summarizes")
	assert.Contains(t, out, "aaa-bbb")
	assert.Contains(t, out, "Deferred")
	assert.Contains(t, out, "tags: alpha, beta")
	assert.Contains(t, out, "trigger: the v2 API ships")
}
