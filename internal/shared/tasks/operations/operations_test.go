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

// TestResolveProjectIdentity_UnchangedForCoordinator pins ResolveProjectIdentity's
// behavior for its coordinator caller (internal/cli/coord_host.go, which derives
// the coordinator state-dir key from it -- an exclusive owner.pid lock): a
// linked git worktree and its primary checkout must resolve to DIFFERENT
// project ids, exactly as before the task-store worktree redirect (task
// brown-canal, 2026-07-10). The task-store seam (workdir.ResolveBoundary /
// projectroot.TaskStoreRoot) lives entirely outside this package specifically
// so this function never has to choose between its two callers' conflicting
// needs -- see internal/cli/run.go, which redirects its OWN workDir argument
// before calling this same, unmodified function.
func TestResolveProjectIdentity_UnchangedForCoordinator(t *testing.T) {
	taskstest.Isolate(t)
	main, linked := taskstest.RealGitWorktreeFixture(t)

	mainID, _, err := ResolveProjectIdentity(main)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	linkedID, _, err := ResolveProjectIdentity(linked)
	if err != nil {
		t.Fatalf("resolve linked: %v", err)
	}
	if mainID == "" || linkedID == "" {
		t.Fatalf("empty id: main=%q linked=%q", mainID, linkedID)
	}
	if mainID == linkedID {
		t.Fatalf("ResolveProjectIdentity must stay worktree-distinct for the coordinator caller, got the same id %q for both %s and %s", mainID, main, linked)
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

// TestListTasksHiddenMatchCounts pins the anti-silent-truncation contract:
// when the default active-only view suppresses tasks that matched the
// requested filters, the result says how many and of what kind, so frontends
// can tell the user instead of letting matches vanish without a trace.
func TestListTasksHiddenMatchCounts(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	active, err := AddTaskWithTags(tc, "active work", "", "", []string{"release"})
	if err != nil {
		t.Fatalf("add active: %v", err)
	}
	done, err := AddTaskWithTags(tc, "finished work", "", "", []string{"release"})
	if err != nil {
		t.Fatalf("add done: %v", err)
	}
	if _, err := SetTaskStatus(tc, done.Task.HarpID, "Done", ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	deferred, err := AddTaskWithTags(tc, "parked work", "", "", []string{"release"})
	if err != nil {
		t.Fatalf("add deferred: %v", err)
	}
	if _, err := SetTaskStatus(tc, deferred.Task.HarpID, "Deferred", "the trigger"); err != nil {
		t.Fatalf("defer: %v", err)
	}

	// Default view + tag query: one visible, one completed + one deferred hidden.
	res, err := ListTasksWithTagQuery(tc, nil, "", "release", false, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].HarpID != active.Task.HarpID {
		t.Fatalf("visible = %+v, want only %s", res.Tasks, active.Task.HarpID)
	}
	if res.HiddenCompleted != 1 || res.HiddenDeferred != 1 {
		t.Fatalf("hidden = completed:%d deferred:%d, want 1/1", res.HiddenCompleted, res.HiddenDeferred)
	}

	// includeDone disables the view filter: nothing hidden, all three shown.
	res, err = ListTasksWithTagQuery(tc, nil, "", "release", true, false)
	if err != nil {
		t.Fatalf("list --all: %v", err)
	}
	if len(res.Tasks) != 3 || res.HiddenCompleted != 0 || res.HiddenDeferred != 0 {
		t.Fatalf("all view = %d tasks, hidden %d/%d; want 3, 0/0", len(res.Tasks), res.HiddenCompleted, res.HiddenDeferred)
	}

	// An explicit status filter is honored verbatim: no view filter, no counts.
	res, err = ListTasksWithTagQuery(tc, []string{"Done"}, "", "release", false, false)
	if err != nil {
		t.Fatalf("list --status Done: %v", err)
	}
	if len(res.Tasks) != 1 || res.HiddenCompleted != 0 || res.HiddenDeferred != 0 {
		t.Fatalf("status view = %d tasks, hidden %d/%d; want 1, 0/0", len(res.Tasks), res.HiddenCompleted, res.HiddenDeferred)
	}
}

// TestListTagCounts pins the tag-enumeration surface: every tag in use is
// reported once, sorted by name, with active (default-view-visible) and total
// counts folding completed and Deferred tasks.
func TestListTagCounts(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	// No tags yet: empty, not nil-error.
	res, err := ListTagCounts(tc)
	if err != nil {
		t.Fatalf("list tags (empty store): %v", err)
	}
	if len(res.Tags) != 0 {
		t.Fatalf("tags of empty store = %+v", res.Tags)
	}
	if res.ProjectID != "p" {
		t.Fatalf("project id = %q, want the pinned id", res.ProjectID)
	}

	if _, err := AddTaskWithTags(tc, "active urgent+release", "", "", []string{"urgent", "release"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	done, err := AddTaskWithTags(tc, "finished release", "", "", []string{"release"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := SetTaskStatus(tc, done.Task.HarpID, "Done", ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	deferred, err := AddTaskWithTags(tc, "parked release", "", "", []string{"release"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := SetTaskStatus(tc, deferred.Task.HarpID, "Deferred", "the trigger"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if _, err := AddTask(tc, "untagged", "", ""); err != nil {
		t.Fatalf("add untagged: %v", err)
	}

	res, err = ListTagCounts(tc)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	want := []TagCount{
		{Tag: "release", Active: 1, Total: 3},
		{Tag: "urgent", Active: 1, Total: 1},
	}
	if len(res.Tags) != len(want) {
		t.Fatalf("tags = %+v, want %+v", res.Tags, want)
	}
	for i, w := range want {
		if res.Tags[i] != w {
			t.Fatalf("tags[%d] = %+v, want %+v (full: %+v)", i, res.Tags[i], w, res.Tags)
		}
	}
}
