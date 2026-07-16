package operations

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestAddAndListTasks_LogPathAndOrigin covers the in-session path: a supplied
// project-id keys the per-project log, the session harp is stamped as origin,
// and a later list folds the same log.
func TestAddAndListTasks_LogPathAndOrigin(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "test-project", SessionHarp: "swift-amber-falcon"}

	add, err := AddTask(tc, "write the docs", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if add.Task.OriginSession != "swift-amber-falcon" {
		t.Fatalf("origin = %q, want the session harp", add.Task.OriginSession)
	}

	logPath, err := paths.TasksLogPath("test-project")
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log not written at %s: %v", logPath, err)
	}

	list, err := ListTasks(tc, nil, "", false, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].HarpID != add.Task.HarpID {
		t.Fatalf("list = %+v", list.Tasks)
	}
	if list.Summary == nil || list.Summary.Counts["To Do"] != 1 {
		t.Fatalf("summary = %+v", list.Summary)
	}
	if list.ProjectID != "test-project" {
		t.Fatalf("project id = %q, want the pinned id", list.ProjectID)
	}
}

// TestResolveLiveMintsIdentity covers the bare-CLI path: with no project-id,
// the store resolves identity live and mints/marks the project on first sight.
func TestResolveLiveMintsIdentity(t *testing.T) {
	taskstest.Isolate(t)
	proj := t.TempDir()
	tc := TaskContext{WorkDir: proj} // no ProjectID → live resolve

	if _, err := AddTask(tc, "a task", "", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	id, err := projectid.ReadMarker(proj)
	if err != nil || id == "" {
		t.Fatalf("marker not minted into project dir: id=%q err=%v", id, err)
	}
}

func TestResolveProjectIdentity(t *testing.T) {
	taskstest.Isolate(t)
	proj := t.TempDir()

	pid, _, err := ResolveProjectIdentity(proj)
	if err != nil || pid == "" {
		t.Fatalf("resolve: pid=%q err=%v", pid, err)
	}
	if got, _ := projectid.ReadMarker(proj); got != pid {
		t.Fatalf("marker %q != resolved pid %q", got, pid)
	}
}

func TestSetTaskStatusThroughOperations(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	add, err := AddTask(tc, "ship it", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	res, err := SetTaskStatus(tc, add.Task.HarpID, "Done", "")
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
	if res.Task.Status != "Done" || !res.Task.Checked {
		t.Fatalf("status = %+v", res.Task)
	}
}

// TestDeferredSinceThroughOperations pins the operations-layer wrapper: it
// resolves the same project store as the other TaskContext-driven calls and
// surfaces only currently Deferred tasks.
func TestDeferredSinceThroughOperations(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	deferred, err := AddTask(tc, "park me", "Deferred", "when x happens")
	if err != nil {
		t.Fatalf("add deferred: %v", err)
	}
	active, err := AddTask(tc, "keep going", "", "")
	if err != nil {
		t.Fatalf("add active: %v", err)
	}

	since, err := DeferredSince(tc)
	if err != nil {
		t.Fatalf("deferred since: %v", err)
	}
	if _, ok := since[deferred.Task.HarpID]; !ok {
		t.Fatalf("deferred task missing from DeferredSince: %+v", since)
	}
	if _, ok := since[active.Task.HarpID]; ok {
		t.Fatalf("non-deferred task must not appear in DeferredSince: %+v", since)
	}
}

// TestPinnedProjectMismatchWarns covers the cross-project warning: acting on a
// pinned project-id while the cwd's marker names a different project must
// surface a notice rather than silently filing the task elsewhere.
func TestPinnedProjectMismatchWarns(t *testing.T) {
	taskstest.Isolate(t)
	cwd := t.TempDir()
	if err := projectid.WriteMarker(cwd, "other-project"); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	res, err := AddTask(TaskContext{WorkDir: cwd, ProjectID: "pinned-project"}, "misfiled?", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Warning == "" {
		t.Fatal("expected a pinned-vs-cwd project mismatch warning")
	}
	if res.ProjectID != "pinned-project" {
		t.Fatalf("project id = %q, want the pin to win", res.ProjectID)
	}
}

// TestAddTaskWithTagsThenListTagQuery covers the whole tag-query wiring at
// the layer both the CLI and MCP surfaces call into: AddTaskWithTags stamps
// initial tags, TagTask adds/removes on an existing task, and
// ListTasksWithTagQuery filters using pkg/tagquery's postfix grammar.
func TestAddTaskWithTagsThenListTagQuery(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	both, err := AddTaskWithTags(tc, "urgent release work", "", "", []string{"urgent", "release"})
	if err != nil {
		t.Fatalf("add with tags: %v", err)
	}
	if len(both.Task.Tags) != 2 {
		t.Fatalf("Tags = %v, want [release urgent]", both.Task.Tags)
	}

	if _, err := AddTaskWithTags(tc, "just urgent", "", "", []string{"urgent"}); err != nil {
		t.Fatalf("add urgent-only: %v", err)
	}

	untagged, err := AddTask(tc, "no tags", "", "")
	if err != nil {
		t.Fatalf("add untagged: %v", err)
	}

	list, err := ListTasksWithTagQuery(tc, nil, "", "urgent/release/and", true, false)
	if err != nil {
		t.Fatalf("list urgent/release/and: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].HarpID != both.Task.HarpID {
		t.Fatalf("and-query = %+v, want only %s", list.Tasks, both.Task.HarpID)
	}

	list, err = ListTasksWithTagQuery(tc, nil, "", "urgent/release/or", true, false)
	if err != nil {
		t.Fatalf("list urgent/release/or: %v", err)
	}
	if len(list.Tasks) != 2 {
		t.Fatalf("or-query = %+v, want both/onlyUrgent", list.Tasks)
	}

	// TagTask: remove "release" from `both`, add "release" to `untagged`.
	if _, err := TagTask(tc, both.Task.HarpID, nil, []string{"release"}); err != nil {
		t.Fatalf("tag task remove: %v", err)
	}
	if _, err := TagTask(tc, untagged.Task.HarpID, []string{"release"}, nil); err != nil {
		t.Fatalf("tag task add: %v", err)
	}

	list, err = ListTasksWithTagQuery(tc, nil, "", "release", true, false)
	if err != nil {
		t.Fatalf("list release: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].HarpID != untagged.Task.HarpID {
		t.Fatalf("post-retag release-query = %+v, want only %s", list.Tasks, untagged.Task.HarpID)
	}

	// Plain ListTasks (no tag query) is unaffected — still sees all three.
	list, err = ListTasks(tc, nil, "", true, false)
	if err != nil {
		t.Fatalf("list (no tag query): %v", err)
	}
	if len(list.Tasks) != 3 {
		t.Fatalf("ListTasks without a tag query = %+v, want all 3 tasks", list.Tasks)
	}
}

// TestListTasksWithTagQueryMalformedErrors pins the fail-loud contract at the
// operations layer: a malformed tag query is a user-facing error, not a
// silently empty or unfiltered listing.
func TestListTasksWithTagQueryMalformedErrors(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p"}
	if _, err := AddTask(tc, "a task", "", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := ListTasksWithTagQuery(tc, nil, "", "and", true, false); err == nil {
		t.Fatal("expected an error for a malformed tag query")
	}
}

// TestTagTaskRequiresAddOrRemove pins the fail-loud contract: calling
// TagTask with neither add nor remove tags is rejected rather than a silent
// no-op mutation.
func TestTagTaskRequiresAddOrRemove(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p"}
	add, err := AddTask(tc, "a task", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := TagTask(tc, add.Task.HarpID, nil, nil); err == nil {
		t.Fatal("expected an error calling TagTask with no add/remove tags")
	}
}
