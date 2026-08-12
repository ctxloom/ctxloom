package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
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

	logPath, err := paths.HomeTasksLogPath("test-project")
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log not written at %s: %v", logPath, err)
	}

	list, err := ListTasks(tc, ListOptions{IncludeSummary: true})
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
// behavior for its coordinator caller (internal/mcp/coord_host.go, which derives
// the coordinator state-dir key from it -- an exclusive owner.pid lock): a
// linked git worktree and its primary checkout must resolve to DIFFERENT
// project ids, exactly as before the task-store worktree redirect (2026-07-10).
// The task-store seam (workdir.ResolveBoundary /
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

// TestMissingLogWarnsWhenSiblingIDHasLogAtSamePath is the reproduction and
// regression test for the silent-empty-backlog design gap:
// a project-id resolves CLEANLY (no pin-vs-cwd disagreement — the existing
// mismatch check in resolveTaskStore has nothing to fire on), but its own
// task log was never written, while a DIFFERENT id also registered at the
// exact same path already has a real one. Before the fix, ListTasks silently
// returned zero tasks with no warning — indistinguishable from a genuinely
// empty project. The assertion that matters is the WARNING PAYLOAD naming
// the sibling id, not just a non-empty string or exit success.
func TestMissingLogWarnsWhenSiblingIDHasLogAtSamePath(t *testing.T) {
	taskstest.Isolate(t)
	cwd := t.TempDir()

	pm, err := projectid.Open("")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if _, err := pm.Adopt("empty-id", cwd); err != nil {
		t.Fatalf("adopt empty-id: %v", err)
	}
	if _, err := pm.Adopt("full-id", cwd); err != nil {
		t.Fatalf("adopt full-id: %v", err)
	}

	// full-id already carries a real backlog at this exact path.
	if _, err := AddTask(TaskContext{WorkDir: cwd, ProjectID: "full-id"}, "the real backlog lives here", "", ""); err != nil {
		t.Fatalf("seed full-id: %v", err)
	}

	// A session resolves (or is pinned) to empty-id instead -- its own log
	// was never written, but nothing here DISAGREES about identity, so the
	// existing pinned-vs-cwd check stays silent.
	res, err := ListTasks(TaskContext{WorkDir: cwd, ProjectID: "empty-id"}, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Tasks) != 0 {
		t.Fatalf("expected an empty list for empty-id (its log was never written), got %+v", res.Tasks)
	}
	if res.Warning == "" {
		t.Fatal("expected a warning naming the sibling project whose log actually has the backlog, got none -- this is the silent-empty-backlog bug")
	}
	if !strings.Contains(res.Warning, "full-id") {
		t.Fatalf("warning = %q, want it to name full-id (the sibling with the real log)", res.Warning)
	}
}

// TestMissingLogStaysSilentWithoutASibling covers the overwhelmingly common
// case: a resolved project genuinely has no tasks yet, and no OTHER id is
// registered at the same path. missingLogSiblingNote must stay a no-op here
// -- a project's very first `taskloom list` before its first `add` must not
// print a spurious warning about a backlog that doesn't exist anywhere.
func TestMissingLogStaysSilentWithoutASibling(t *testing.T) {
	taskstest.Isolate(t)
	cwd := t.TempDir()

	pm, err := projectid.Open("")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if _, err := pm.Adopt("only-id", cwd); err != nil {
		t.Fatalf("adopt only-id: %v", err)
	}

	res, err := ListTasks(TaskContext{WorkDir: cwd, ProjectID: "only-id"}, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Tasks) != 0 {
		t.Fatalf("expected an empty list, got %+v", res.Tasks)
	}
	if res.Warning != "" {
		t.Fatalf("warning = %q, want none: a genuinely empty project with no sibling must stay silent", res.Warning)
	}
}

// TestAddTaskWithTagsThenListTagQuery covers the whole tag-query wiring at
// the layer both the CLI and MCP surfaces call into: AddTaskWithTags stamps
// initial tags, TagTask adds/removes on an existing task, and
// ListTasks filters using tagma's postfix grammar.
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

	list, err := ListTasks(tc, ListOptions{TagQuery: "urgent/release/and", IncludeDone: true})
	if err != nil {
		t.Fatalf("list urgent/release/and: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].HarpID != both.Task.HarpID {
		t.Fatalf("and-query = %+v, want only %s", list.Tasks, both.Task.HarpID)
	}

	list, err = ListTasks(tc, ListOptions{TagQuery: "urgent/release/or", IncludeDone: true})
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

	list, err = ListTasks(tc, ListOptions{TagQuery: "release", IncludeDone: true})
	if err != nil {
		t.Fatalf("list release: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].HarpID != untagged.Task.HarpID {
		t.Fatalf("post-retag release-query = %+v, want only %s", list.Tasks, untagged.Task.HarpID)
	}

	// Plain ListTasks (no tag query) is unaffected — still sees all three.
	list, err = ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list (no tag query): %v", err)
	}
	if len(list.Tasks) != 3 {
		t.Fatalf("ListTasks without a tag query = %+v, want all 3 tasks", list.Tasks)
	}
}

// TestListTasksTagQueryMalformedErrors pins the fail-loud contract at the
// operations layer: a malformed tag query is a user-facing error, not a
// silently empty or unfiltered listing.
func TestListTasksTagQueryMalformedErrors(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p"}
	if _, err := AddTask(tc, "a task", "", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := ListTasks(tc, ListOptions{TagQuery: "and", IncludeDone: true}); err == nil {
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
	res, err := ListTasks(tc, ListOptions{TagQuery: "release"})
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
	res, err = ListTasks(tc, ListOptions{TagQuery: "release", IncludeDone: true})
	if err != nil {
		t.Fatalf("list --all: %v", err)
	}
	if len(res.Tasks) != 3 || res.HiddenCompleted != 0 || res.HiddenDeferred != 0 {
		t.Fatalf("all view = %d tasks, hidden %d/%d; want 3, 0/0", len(res.Tasks), res.HiddenCompleted, res.HiddenDeferred)
	}

	// An explicit status filter is honored verbatim: no view filter, no counts.
	res, err = ListTasks(tc, ListOptions{Statuses: []string{"Done"}, TagQuery: "release"})
	if err != nil {
		t.Fatalf("list --status Done: %v", err)
	}
	if len(res.Tasks) != 1 || res.HiddenCompleted != 0 || res.HiddenDeferred != 0 {
		t.Fatalf("status view = %d tasks, hidden %d/%d; want 1, 0/0", len(res.Tasks), res.HiddenCompleted, res.HiddenDeferred)
	}
}

// TestListTasksLimitCapsRowsButNotSummaryCounts pins the CRITICAL contract of
// `limit`: it caps the row count of Tasks (applied after status/term/tag
// filtering and the default active-only pass), reports how many it cut in
// OmittedByLimit, but leaves include_summary's counts (which fold EVERY task)
// completely uncapped — a caller must never mistake a truncated page for a
// truncated project.
func TestListTasksLimitCapsRowsButNotSummaryCounts(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	const total = 5
	for i := 0; i < total; i++ {
		if _, err := AddTask(tc, fmt.Sprintf("task number %d", i), "", ""); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	// limit=0 (default): no cap, today's exact behavior.
	res, err := ListTasks(tc, ListOptions{IncludeSummary: true})
	if err != nil {
		t.Fatalf("list limit=0: %v", err)
	}
	if len(res.Tasks) != total {
		t.Fatalf("limit=0 rows = %d, want all %d", len(res.Tasks), total)
	}
	if res.OmittedByLimit != 0 {
		t.Fatalf("limit=0 OmittedByLimit = %d, want 0", res.OmittedByLimit)
	}
	if res.Summary == nil || res.Summary.Counts["To Do"] != total {
		t.Fatalf("summary = %+v, want To Do:%d", res.Summary, total)
	}

	// limit=2: only 2 rows come back, 3 are reported omitted, but the
	// summary's counts still cover all 5 — the query layer's include_summary
	// is computed independently of the (now-truncated) row list.
	const limit = 2
	res, err = ListTasks(tc, ListOptions{IncludeSummary: true, Limit: limit})
	if err != nil {
		t.Fatalf("list limit=%d: %v", limit, err)
	}
	if len(res.Tasks) != limit {
		t.Fatalf("limit=%d rows = %d, want %d", limit, len(res.Tasks), limit)
	}
	if want := total - limit; res.OmittedByLimit != want {
		t.Fatalf("OmittedByLimit = %d, want %d", res.OmittedByLimit, want)
	}
	if res.Summary == nil || res.Summary.Counts["To Do"] != total {
		t.Fatalf("summary under limit=%d = %+v, want the FULL uncapped To Do:%d", limit, res.Summary, total)
	}

	// limit larger than the result: no truncation, nothing omitted.
	res, err = ListTasks(tc, ListOptions{Limit: total + 10})
	if err != nil {
		t.Fatalf("list limit>total: %v", err)
	}
	if len(res.Tasks) != total || res.OmittedByLimit != 0 {
		t.Fatalf("limit>total rows=%d omitted=%d, want %d/0", len(res.Tasks), res.OmittedByLimit, total)
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

// TestHoming_RepoHomedWritesInsideRepo proves TaskContext.HomingMode ==
// paths.ModeRepo lands the task log INSIDE the project tree, at
// paths.RepoDirName/paths.RepoTasksFileName (.taskloom/tasks.jsonl), and
// never touches the isolated home directory — the store is meant to be
// checked in and shareable, not private. It also pins the project-id
// question from the task brief: repo-homed mode mints no id at all (the
// repo path itself is the identity), so ProjectID comes back empty.
func TestHoming_RepoHomedWritesInsideRepo(t *testing.T) {
	home := taskstest.Isolate(t)
	proj := t.TempDir()
	tc := TaskContext{WorkDir: proj, SessionHarp: "sess", HomingMode: paths.ModeRepo}

	res, err := AddTask(tc, "ship it", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	wantPath := filepath.Join(proj, paths.RepoDirName, paths.RepoTasksFileName)
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("repo-homed log not written at %s: %v", wantPath, err)
	}
	if res.ProjectID != "" {
		t.Fatalf("ProjectID = %q, want empty (repo-homed mints no id)", res.ProjectID)
	}
	if res.ProjectDir != proj {
		t.Fatalf("ProjectDir = %q, want %q", res.ProjectDir, proj)
	}

	// Never leaked into the (isolated) home tasks dir.
	homeTasksDir := filepath.Join(home, paths.AppDirName, paths.TasksDir)
	if entries, err := os.ReadDir(homeTasksDir); err == nil && len(entries) != 0 {
		t.Fatalf("repo-homed add leaked into home tasks dir: %v", entries)
	}
}

// TestHoming_HomeHomedWritesUnderHome pins today's (pre-homing) behavior for
// TaskContext.HomingMode == paths.ModeHome given EXPLICITLY (as
// cmd/taskloom's resolved mode always is) — not just the zero-value default
// TestResolveLiveMintsIdentity/TestAddAndListTasks_LogPathAndOrigin already
// cover.
func TestHoming_HomeHomedWritesUnderHome(t *testing.T) {
	home := taskstest.Isolate(t)
	proj := t.TempDir()
	tc := TaskContext{WorkDir: proj, SessionHarp: "sess", HomingMode: paths.ModeHome}

	res, err := AddTask(tc, "ship it", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.ProjectID == "" {
		t.Fatal("ProjectID empty, want a minted/registered id")
	}
	wantPath, err := paths.HomeTasksLogPath(res.ProjectID)
	if err != nil {
		t.Fatalf("HomeTasksLogPath: %v", err)
	}
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if !strings.HasPrefix(wantPath, filepath.Join(home, paths.AppDirName)) {
		t.Fatalf("home-homed log %q is not under the isolated home %q", wantPath, home)
	}
}

// TestAddTaskWithTagsRejectsUnqueryableTags pins the write-seam guard: a tag
// containing "/" (outside tagma's bare-token charset) or a reserved operator
// word ("and"/"or"/"not") would be permanently unqueryable once stored, so
// AddTaskWithTags must reject the whole operation before any event is
// written — no task, no partial tag set left behind.
func TestAddTaskWithTagsRejectsUnqueryableTags(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	if _, err := AddTaskWithTags(tc, "bad slash tag", "", "", []string{"urgent", "foo/bar"}); err == nil {
		t.Fatal("expected an error adding a task with a slash-bearing tag")
	}
	if _, err := AddTaskWithTags(tc, "bad reserved tag", "", "", []string{"and"}); err == nil {
		t.Fatal("expected an error adding a task with a reserved-word tag")
	}

	// Neither rejected call should have written anything: the store stays
	// empty.
	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 0 {
		t.Fatalf("expected no tasks written after rejected adds, got %+v", list.Tasks)
	}
}

// TestTagTaskRejectsUnqueryableAddedTags pins the same guard on the other add
// seam: TagTask's `add` list must be rejected before the event is appended,
// leaving the task's existing tags untouched. Removing tags is unaffected —
// subtracting a malformed tag never creates new unqueryable data.
func TestTagTaskRejectsUnqueryableAddedTags(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}

	add, err := AddTaskWithTags(tc, "a task", "", "", []string{"urgent"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := TagTask(tc, add.Task.HarpID, []string{"foo/bar"}, nil); err == nil {
		t.Fatal("expected an error adding a slash-bearing tag via TagTask")
	}
	if _, err := TagTask(tc, add.Task.HarpID, []string{"or"}, nil); err == nil {
		t.Fatal("expected an error adding a reserved-word tag via TagTask")
	}

	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 1 || len(list.Tasks[0].Tags) != 1 || list.Tasks[0].Tags[0] != "urgent" {
		t.Fatalf("tags after rejected TagTask add = %+v, want unchanged [urgent]", list.Tasks)
	}

	// Removing tags is not subject to this guard.
	if _, err := TagTask(tc, add.Task.HarpID, nil, []string{"urgent"}); err != nil {
		t.Fatalf("remove should be unaffected by the add-side guard: %v", err)
	}
}

// TestValidateTagRejectsReservedWordsCaseInsensitively pins the
// tagma-aware write guard's case-insensitive reserved-word rule: tagma's
// grammar was deliberately extended to match "and"/"or"/"not" as operators
// regardless of case (mirroring the retired pkg/tagquery's own leniency),
// so a tag spelled "AND" is exactly as unqueryable as "and" and must be
// rejected the same way.
func TestValidateTagRejectsReservedWordsCaseInsensitively(t *testing.T) {
	for _, tag := range []string{"and", "AND", "And", "or", "OR", "not", "NOT", " and "} {
		if err := validateTag(tag, nil); err == nil {
			t.Errorf("validateTag(%q): expected an error (reserved operator word)", tag)
		}
	}
}

// TestValidateTagRejectsTagmaUnparseableTags pins the new-under-tagma half
// of the write guard: tagma's bare-token grammar is narrower than the old
// pkg/tagquery check (which only rejected "/" and reserved words) — it also
// rejects "*", "/", and embedded whitespace, none of which tagma.ParseTag
// can turn into a matchable atom.
//
// "+" is deliberately NOT rejected here any more. Both signs became
// ordinary bare-token characters in tagma 10300d4, so SemVer build
// metadata (triage:blocks-release=1.0.0+build.5) is writable unquoted.
// "+" stays reserved only as a WHOLE token, where it is the quantifier —
// hence the bare "+" case below, while "release+" is now an ordinary tag.
func TestValidateTagRejectsTagmaUnparseableTags(t *testing.T) {
	for _, tag := range []string{"urgent*", "+", "*", "foo/bar", "has space"} {
		if err := validateTag(tag, nil); err == nil {
			t.Errorf("validateTag(%q): expected an error (not a valid tagma tag)", tag)
		}
	}
}

// TestValidateTagAcceptsOrdinaryTags pins the non-rejection side: plain
// bare-token tags, including ones that merely start with or contain a
// reserved word as a substring, must stay valid.
func TestValidateTagAcceptsOrdinaryTags(t *testing.T) {
	for _, tag := range []string{"urgent", "release", "android", "cannot", "note", "north", "v1.2", "a-b_c", "release+", "1.0.0+build.5", "+1", "-1"} {
		if err := validateTag(tag, nil); err != nil {
			t.Errorf("validateTag(%q): unexpected error: %v", tag, err)
		}
	}
}

// --- write-seam enum/range enforcement (validateTag's schema-aware half) ---

// triageValueSchema returns a *tagschema.Schema declaring a closed enum for
// triage:kind and a numeric range for triage:effort — a minimal slice of
// the shipped DefaultTagSchema, enough to exercise validateTag's enum/range
// branches without depending on taskloomconfig.
func triageValueSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	schema, err := tagschema.Parse([]string{
		`tagma.enum:"triage:kind"="defect,capability,chore"`,
		`tagma.range:"triage:effort"="0,5"`,
	})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// TestValidateTagAcceptsDeclaredEnumValue and
// TestValidateTagRejectsDeclaredEnumViolation pin validateTag's new
// enum-checking half directly: a value that IS a member of a declared enum
// passes; one that isn't is rejected with an error naming the target and
// the declared members.
func TestValidateTagAcceptsDeclaredEnumValue(t *testing.T) {
	schema := triageValueSchema(t)
	if err := validateTag("triage:kind=defect", schema); err != nil {
		t.Errorf("validateTag(triage:kind=defect): unexpected error: %v", err)
	}
}

func TestValidateTagRejectsDeclaredEnumViolation(t *testing.T) {
	schema := triageValueSchema(t)
	err := validateTag("triage:kind=sparkles", schema)
	if err == nil {
		t.Fatal("validateTag(triage:kind=sparkles): expected an error (not a declared enum member)")
	}
	if !strings.Contains(err.Error(), "triage:kind") || !strings.Contains(err.Error(), "sparkles") {
		t.Errorf("error %q does not name the target and offending value", err)
	}
}

// TestValidateTagAcceptsDeclaredRangeValue and
// TestValidateTagRejectsDeclaredRangeViolation pin the same for the
// range-checking half: in-bounds numeric values pass, out-of-bounds or
// non-numeric ones are rejected.
func TestValidateTagAcceptsDeclaredRangeValue(t *testing.T) {
	schema := triageValueSchema(t)
	if err := validateTag("triage:effort=3", schema); err != nil {
		t.Errorf("validateTag(triage:effort=3): unexpected error: %v", err)
	}
	if err := validateTag("triage:effort=0", schema); err != nil {
		t.Errorf("validateTag(triage:effort=0): unexpected error (range boundary): %v", err)
	}
	if err := validateTag("triage:effort=5", schema); err != nil {
		t.Errorf("validateTag(triage:effort=5): unexpected error (range boundary): %v", err)
	}
}

func TestValidateTagRejectsDeclaredRangeViolation(t *testing.T) {
	schema := triageValueSchema(t)
	if err := validateTag("triage:effort=9", schema); err == nil {
		t.Error("validateTag(triage:effort=9): expected an error (outside the declared range)")
	}
	if err := validateTag("triage:effort=notanumber", schema); err == nil {
		t.Error("validateTag(triage:effort=notanumber): expected an error (doesn't parse as a number under a declared range)")
	}
}

// TestValidateTagRejectsNaNOnDeclaredRange pins the fix: strconv.ParseFloat
// accepts the literal string "NaN" (a legal tagma bare token), and every
// `<`/`>` bounds comparison against NaN is false, so this write-time gate —
// whose entire purpose is "a value that would be permanently unqueryable is
// rejected loudly instead of silently persisted" — let NaN straight through.
// A stored NaN goes on to hang priority.rankNormalize with no
// warning anywhere in the write path.
func TestValidateTagRejectsNaNOnDeclaredRange(t *testing.T) {
	schema := triageValueSchema(t)
	for _, tag := range []string{"triage:effort=NaN", "triage:effort=nan", "triage:effort=Inf", "triage:effort=-Inf"} {
		if err := validateTag(tag, schema); err == nil {
			t.Errorf("validateTag(%q): expected an error (not a finite number)", tag)
		}
	}
}

// blocksReleaseSemverSchema returns a *tagschema.Schema declaring
// triage:blocks-release's tagma.type=semver (tagschema.SemverTypeName) —
// the same declaration DefaultTagSchema ships (tagma SPEC.md §9,
// client-loadable type comparison).
func blocksReleaseSemverSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	schema, err := tagschema.Parse([]string{
		`tagma.type:"triage:blocks-release"=` + tagschema.SemverTypeName,
	})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// TestValidateTagAcceptsDeclaredSemverType and
// TestValidateTagRejectsDeclaredSemverTypeViolation pin validateTag's
// tagma.type=semver branch directly: a value that parses as a strict SemVer
// 2.0.0 version passes; one that doesn't is rejected with an error naming
// the target and the offending value — the write-seam counterpart to
// internal/shared/tasks's registered comparator, so a value this rejects
// could never have matched a relational query on the target either.
func TestValidateTagAcceptsDeclaredSemverType(t *testing.T) {
	// "1.0.0+build.5" (SemVer build metadata) is deliberately not exercised
	// here: tagma's own tag-value grammar has no charset room for '+' in a
	// bare (unquoted) value token, so a task tag can never carry build
	// metadata at all regardless of any tagma.type declaration — validateTag
	// rejects it at the ParseTag step, before this branch is ever reached.
	// Build-metadata EQUALITY (SemVer 2.0.0 §10) is still pinned directly at
	// the comparator (TestSemverComparatorBuildMetadataEquality,
	// internal/shared/tasks), which never goes through tag storage.
	schema := blocksReleaseSemverSchema(t)
	for _, tag := range []string{
		"triage:blocks-release=0.7.0",
		"triage:blocks-release=0.7.0-pre001",
	} {
		if err := validateTag(tag, schema); err != nil {
			t.Errorf("validateTag(%q): unexpected error: %v", tag, err)
		}
	}
}

func TestValidateTagRejectsDeclaredSemverTypeViolation(t *testing.T) {
	schema := blocksReleaseSemverSchema(t)
	for _, tag := range []string{
		"triage:blocks-release=notasemver",
		"triage:blocks-release=0.7",    // strict semver requires major.minor.patch
		"triage:blocks-release=v0.7.0", // strict semver rejects a "v" prefix
	} {
		err := validateTag(tag, schema)
		if err == nil {
			t.Fatalf("validateTag(%q): expected an error (not a valid SemVer 2.0.0 version)", tag)
		}
		if !strings.Contains(err.Error(), "triage:blocks-release") {
			t.Errorf("validateTag(%q): error %q does not name the target", tag, err)
		}
	}
}

// TestAddTaskWithTagsRejectsDeclaredSemverTypeViolation pins the semver-type
// gate at the AddTaskWithTags write seam end to end, mirroring
// TestAddTaskWithTagsRejectsDeclaredEnumViolation for tagma.type=semver: a
// rejected add must persist nothing at all.
func TestAddTaskWithTagsRejectsDeclaredSemverTypeViolation(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: blocksReleaseSemverSchema(t)}

	if _, err := AddTaskWithTags(tc, "bad version", "", "", []string{"triage:blocks-release=notasemver"}); err == nil {
		t.Fatal("expected an error adding a tag whose value isn't a valid SemVer 2.0.0 version")
	}

	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 0 {
		t.Fatalf("expected no tasks written after a rejected semver-type violation, got %+v", list.Tasks)
	}
}

// TestValidateTagSkipsEnumRangeChecksWithNilSchema proves a nil schema (every
// caller that predates this feature) never enforces enum/range at all — the
// same values that TestValidateTagRejectsDeclaredEnumViolation/
// TestValidateTagRejectsDeclaredRangeViolation reject are accepted here.
func TestValidateTagSkipsEnumRangeChecksWithNilSchema(t *testing.T) {
	for _, tag := range []string{"triage:kind=sparkles", "triage:effort=9", "triage:effort=notanumber"} {
		if err := validateTag(tag, nil); err != nil {
			t.Errorf("validateTag(%q, nil): unexpected error: %v", tag, err)
		}
	}
}

// TestAddTaskWithTagsRejectsDeclaredEnumViolation and
// TestAddTaskWithTagsRejectsDeclaredRangeViolation pin the enum/range gate
// at the AddTaskWithTags write seam end to end: a rejected add must persist
// nothing at all, mirroring TestAddTaskWithTagsRejectsUnqueryableTags for
// the structural checks.
func TestAddTaskWithTagsRejectsDeclaredEnumViolation(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: triageValueSchema(t)}

	if _, err := AddTaskWithTags(tc, "bad kind", "", "", []string{"triage:kind=sparkles"}); err == nil {
		t.Fatal("expected an error adding a tag whose value violates the declared enum")
	}

	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 0 {
		t.Fatalf("expected no tasks written after a rejected enum violation, got %+v", list.Tasks)
	}
}

func TestAddTaskWithTagsRejectsDeclaredRangeViolation(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: triageValueSchema(t)}

	if _, err := AddTaskWithTags(tc, "bad effort", "", "", []string{"triage:effort=9"}); err == nil {
		t.Fatal("expected an error adding a tag whose value is outside the declared range")
	}

	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 0 {
		t.Fatalf("expected no tasks written after a rejected range violation, got %+v", list.Tasks)
	}
}

// TestTagTaskRejectsDeclaredEnumViolation mirrors the above on the TagTask
// add seam (see TestTagTaskRejectsUnqueryableAddedTags).
func TestTagTaskRejectsDeclaredEnumViolation(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: triageValueSchema(t)}

	add, err := AddTaskWithTags(tc, "a task", "", "", []string{"urgent"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := TagTask(tc, add.Task.HarpID, []string{"triage:kind=sparkles"}, nil); err == nil {
		t.Fatal("expected an error adding an enum-violating tag via TagTask")
	}

	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tasks) != 1 || len(list.Tasks[0].Tags) != 1 || list.Tasks[0].Tags[0] != "urgent" {
		t.Fatalf("tags after rejected TagTask add = %+v, want unchanged [urgent]", list.Tasks)
	}
}

// TestWriteSeamNeverRevalidatesPreExistingTags pins the CRITICAL constraint
// this whole feature depends on: validateTag/validateTags examine ONLY the
// tags being WRITTEN on a given call, never a task's already-stored tag
// set. Without this, task_set_status/task_edit/task_tag on any of this
// project's 318 live tasks carrying bare unnamespaced legacy tags — or, as
// simulated here, a tag that flatly violates a schema declared after it was
// written — would start failing on writes that never even touch tags.
//
// The offending tag is seeded DIRECTLY through the underlying tasks.Store,
// bypassing validateTag entirely (exactly like real pre-existing/foreign
// data would have been written, before this gate — or this project's
// tag_schema — existed).
func TestWriteSeamNeverRevalidatesPreExistingTags(t *testing.T) {
	taskstest.Isolate(t)
	schema := triageValueSchema(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: schema}

	_, logPath, err := ResolveLogPath(tc)
	if err != nil {
		t.Fatalf("resolve log path: %v", err)
	}
	store, err := tasks.OpenLog(logPath, tc.SessionHarp)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	seeded, err := store.AddWithTags("legacy task", "", "", "triage:kind=sparkles", "legacy-flat-tag")
	if err != nil {
		t.Fatalf("seed legacy task (bypassing the write-seam gate): %v", err)
	}

	// None of these writes touch the offending tags, and none may be
	// rejected on their account.
	if _, err := SetTaskStatus(tc, seeded.HarpID, "In Progress", ""); err != nil {
		t.Fatalf("SetTaskStatus on a task carrying pre-existing schema-violating tags: %v", err)
	}
	if _, err := EditTask(tc, seeded.HarpID, "legacy task, edited"); err != nil {
		t.Fatalf("EditTask on a task carrying pre-existing schema-violating tags: %v", err)
	}
	res, err := TagTask(tc, seeded.HarpID, []string{"urgent"}, nil)
	if err != nil {
		t.Fatalf("TagTask add (of an unrelated, valid tag) on a task carrying pre-existing schema-violating tags: %v", err)
	}
	if !slices.Contains(res.Task.Tags, "urgent") {
		t.Fatalf("tags after add = %+v, want \"urgent\" included", res.Task.Tags)
	}
	if !slices.Contains(res.Task.Tags, "triage:kind=sparkles") {
		t.Fatalf("pre-existing schema-violating tag must survive untouched, got %+v", res.Task.Tags)
	}
	if !slices.Contains(res.Task.Tags, "legacy-flat-tag") {
		t.Fatalf("pre-existing bare legacy tag must survive untouched, got %+v", res.Task.Tags)
	}
}

// TestHoming_ProjectFlagIgnoredInRepoMode proves a pinned --project is a
// harmless no-op (with a warning, not silently swallowed) in repo-homed
// mode: the repo path is the identity, so there is nothing for a
// project-id to select between.
func TestHoming_ProjectFlagIgnoredInRepoMode(t *testing.T) {
	taskstest.Isolate(t)
	proj := t.TempDir()
	tc := TaskContext{WorkDir: proj, ProjectID: "pinned-but-irrelevant", HomingMode: paths.ModeRepo}

	res, err := AddTask(tc, "ship it", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Warning == "" {
		t.Fatal("expected a warning that --project is ignored in repo-homed mode")
	}
	if !strings.Contains(res.Warning, "pinned-but-irrelevant") {
		t.Fatalf("warning %q does not name the ignored project id", res.Warning)
	}
}

// --- tagma-adoption phase 2: tag-schema config + scalar-collapse write seam ---

// scalarSchema returns a *tagschema.Schema declaring triage:type
// arity=scalar, the schema every collapse test below shares.
func scalarSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	schema, err := tagschema.Parse([]string{`tagma.arity:"triage:type"=scalar`})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// readTaskEvents reads tc's raw JSONL log and returns every event whose Task
// field matches harpID, in file order — used to assert on the LOG itself
// (not just the folded Task.Tags view), so a collapse test can prove history
// is preserved (both the untag and the tag survive) even though the fold
// converges to one value.
func readTaskEvents(t *testing.T, tc TaskContext, harpID string) []tasks.Event {
	t.Helper()
	_, logPath, err := ResolveLogPath(tc)
	if err != nil {
		t.Fatalf("resolve log path: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out []tasks.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var ev tasks.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal event line %q: %v", line, err)
		}
		if ev.Task == harpID {
			out = append(out, ev)
		}
	}
	return out
}

// TestScalarCollapse_TagTaskCollapsesToNewestValueHistoryPreserved pins the
// phase-2 write-seam core case: re-tagging a declared-scalar (namespace,
// key) with a DIFFERENT value collapses the task's FOLDED tags down to the
// newest one (last wins) -- but the underlying log still records BOTH the
// retracting untag and the new tag; history is never rewritten, only the
// fold converges.
func TestScalarCollapse_TagTaskCollapsesToNewestValueHistoryPreserved(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: scalarSchema(t)}

	add, err := AddTaskWithTags(tc, "triage this", "", "", []string{"triage:type=security"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	res, err := TagTask(tc, add.Task.HarpID, []string{"triage:type=bug"}, nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if got := strings.Join(res.Task.Tags, ","); got != "triage:type=bug" {
		t.Fatalf("folded tags after collapse = %q, want exactly %q", got, "triage:type=bug")
	}

	events := readTaskEvents(t, tc, add.Task.HarpID)
	var sawUntagSecurity, sawTagBug bool
	for _, ev := range events {
		switch {
		case ev.Op == "untag" && len(ev.Tags) == 1 && ev.Tags[0] == "triage:type=security":
			sawUntagSecurity = true
		case ev.Op == "tag" && len(ev.Tags) == 1 && ev.Tags[0] == "triage:type=bug":
			sawTagBug = true
		}
	}
	if !sawUntagSecurity {
		t.Errorf("log events %+v do not record the retracting untag of triage:type=security", events)
	}
	if !sawTagBug {
		t.Errorf("log events %+v do not record the tag of triage:type=bug", events)
	}
}

// TestTagTask_ScalarCollapseAddsBeforeUntag pins the fix: the log is
// append-only, so "retract the old scalar value, then add the new one" is
// necessarily two separate writes, never one atomic operation. The old
// ordering wrote the retracting untag FIRST — if the following add then
// failed (lock contention, a write error), the task permanently lost its
// previous value and gained nothing, with no compensating write. Add must
// land first: if the untag then fails, the task is left with a transient
// DUPLICATE value on a scalar target (recoverable — lint flags it, and the
// next collapse fixes it), never a LOST one.
func TestTagTask_ScalarCollapseAddsBeforeUntag(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: scalarSchema(t)}

	add, err := AddTaskWithTags(tc, "triage this", "", "", []string{"triage:type=security"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := TagTask(tc, add.Task.HarpID, []string{"triage:type=bug"}, nil); err != nil {
		t.Fatalf("tag: %v", err)
	}

	events := readTaskEvents(t, tc, add.Task.HarpID)
	tagIdx, untagIdx := -1, -1
	for i, ev := range events {
		switch ev.Op {
		case "tag":
			if len(ev.Tags) == 1 && ev.Tags[0] == "triage:type=bug" {
				tagIdx = i
			}
		case "untag":
			if len(ev.Tags) == 1 && ev.Tags[0] == "triage:type=security" {
				untagIdx = i
			}
		}
	}
	if tagIdx == -1 || untagIdx == -1 {
		t.Fatalf("expected both a tag(bug) and untag(security) event, got %+v", events)
	}
	if tagIdx > untagIdx {
		t.Fatalf("the new value's add (event %d) must be written BEFORE the old value's untag (event %d) — a failure between them must leave a recoverable duplicate, never a lost value", tagIdx, untagIdx)
	}
}

// TestScalarCollapse_IdenticalValueEmitsNoUntag proves re-tagging a
// declared-scalar key with the SAME value is a pure no-op collapse-wise: no
// retracting untag is ever emitted for a value that didn't change.
func TestScalarCollapse_IdenticalValueEmitsNoUntag(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: scalarSchema(t)}

	add, err := AddTaskWithTags(tc, "triage this too", "", "", []string{"triage:type=security"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	res, err := TagTask(tc, add.Task.HarpID, []string{"triage:type=security"}, nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if got := strings.Join(res.Task.Tags, ","); got != "triage:type=security" {
		t.Fatalf("folded tags = %q, want exactly %q", got, "triage:type=security")
	}
	for _, ev := range readTaskEvents(t, tc, add.Task.HarpID) {
		if ev.Op == "untag" {
			t.Fatalf("re-tagging the identical value must never emit an untag, got %+v", ev)
		}
	}
}

// TestScalarCollapse_UndeclaredKeyKeepsMultipleValues proves a key the
// schema does NOT declare scalar is never collapsed: every distinct value
// accumulates exactly like a plain flat tag always has.
func TestScalarCollapse_UndeclaredKeyKeepsMultipleValues(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: scalarSchema(t)}

	add, err := AddTaskWithTags(tc, "track cwes", "", "", []string{"triage:cwe=79"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	res, err := TagTask(tc, add.Task.HarpID, []string{"triage:cwe=89"}, nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if got := strings.Join(res.Task.Tags, ","); got != "triage:cwe=79,triage:cwe=89" {
		t.Fatalf("tags = %q, want both undeclared-key values preserved", got)
	}
}

// TestScalarCollapse_InitialTagsCollapseToLastValue proves the SAME
// last-wins rule applies to AddTaskWithTags's own initial tag list: when the
// list itself carries two values for the same scalar target, only the last
// (in list order) survives onto the brand-new task -- there is no existing
// task yet to retract a value from, so this is pure intra-list dedup.
func TestScalarCollapse_InitialTagsCollapseToLastValue(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: scalarSchema(t)}

	add, err := AddTaskWithTags(tc, "dup initial tags", "", "", []string{"triage:type=security", "urgent", "triage:type=bug"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := strings.Join(add.Task.Tags, ","); got != "triage:type=bug,urgent" {
		t.Fatalf("initial tags after collapse = %q, want %q", got, "triage:type=bug,urgent")
	}
}

// TestScalarCollapse_NilSchemaNeverCollapses proves TagSchema's zero value
// (nil -- every pre-phase-2 caller) disables collapse entirely: a task can
// still carry more than one value for what WOULD be a scalar target
// elsewhere, exactly today's pre-tag-schema behavior.
func TestScalarCollapse_NilSchemaNeverCollapses(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"} // TagSchema left nil

	add, err := AddTaskWithTags(tc, "no schema here", "", "", []string{"triage:type=security"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	res, err := TagTask(tc, add.Task.HarpID, []string{"triage:type=bug"}, nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if got := strings.Join(res.Task.Tags, ","); got != "triage:type=bug,triage:type=security" {
		t.Fatalf("tags = %q, want both values uncollapsed with no schema", got)
	}
}

// TestValidateTagRejectsTagmaNamespace proves a task tag in the reserved
// "tagma" (or "tagma.<facet>") namespace is rejected: that namespace is
// config-only (tag_schema declarations), and a task write must never be
// able to inject into it.
func TestValidateTagRejectsTagmaNamespace(t *testing.T) {
	for _, tag := range []string{`tagma.arity:"triage:type"=scalar`, "tagma:foo", "tagma.priority_fn:x=y"} {
		if err := validateTag(tag, nil); err == nil {
			t.Errorf("validateTag(%q): expected an error (reserved tagma namespace)", tag)
		}
	}
}

// TestValidateTagAcceptsNamespaceMerelyPrefixedWithTagma proves the
// reserved-namespace guard matches "tagma" or "tagma.<facet>" EXACTLY, not
// any namespace that merely starts with the same letters -- "tagmatic" is a
// perfectly ordinary namespace.
func TestValidateTagAcceptsNamespaceMerelyPrefixedWithTagma(t *testing.T) {
	if err := validateTag("tagmatic:foo=bar", nil); err != nil {
		t.Errorf("validateTag(tagmatic:foo=bar): unexpected error: %v", err)
	}
}

// TestAddTaskWithTagsRejectsTagmaNamespaceTag and
// TestTagTaskRejectsTagmaNamespaceTag pin the guard at both write seams
// (mirroring TestAddTaskWithTagsRejectsUnqueryableTags/
// TestTagTaskRejectsUnqueryableAddedTags for the reserved-namespace rule
// specifically): a user-supplied tag naming the tagma namespace must be
// rejected before any event is written.
func TestAddTaskWithTagsRejectsTagmaNamespaceTag(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}
	if _, err := AddTaskWithTags(tc, "should fail", "", "", []string{"tagma.arity:x=y"}); err == nil {
		t.Fatal("expected an error adding a task tag in the reserved tagma namespace")
	}
}

func TestTagTaskRejectsTagmaNamespaceTag(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess"}
	add, err := AddTaskWithTags(tc, "a task", "", "", []string{"urgent"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := TagTask(tc, add.Task.HarpID, []string{"tagma.arity:x=y"}, nil); err == nil {
		t.Fatal("expected an error adding a tagma-namespaced tag via TagTask")
	}
}

func priorityFormulaSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	schema, err := tagschema.Parse([]string{
		`tagma.priority_fn:"triage:impact"="{{triage:impact}}"`,
	})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// TestComputeTaskPriorities_RanksAcrossTheFullProjectSnapshot proves the
// scoping decision this feature made deliberately: percentile rank is
// computed over the store's FULL current snapshot, not just whatever a
// caller happens to be displaying — ComputeTaskPriorities takes no
// term/tag-query/status filter at all, unlike ListTasks*.
func TestComputeTaskPriorities_RanksAcrossTheFullProjectSnapshot(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: priorityFormulaSchema(t)}

	low, err := AddTaskWithTags(tc, "low", "", "", []string{"triage:impact=1"})
	if err != nil {
		t.Fatalf("add low: %v", err)
	}
	high, err := AddTaskWithTags(tc, "high", "", "", []string{"triage:impact=9"})
	if err != nil {
		t.Fatalf("add high: %v", err)
	}

	results, _, err := ComputeTaskPriorities(tc, time.Now())
	if err != nil {
		t.Fatalf("compute priorities: %v", err)
	}
	if results[low.Task.HarpID].Priority >= results[high.Task.HarpID].Priority {
		t.Fatalf("expected low's priority (%v) below high's (%v)",
			results[low.Task.HarpID].Priority, results[high.Task.HarpID].Priority)
	}
	if results[high.Task.HarpID].Priority != 5.0 {
		t.Fatalf("the population maximum should reach the ceiling, got %v", results[high.Task.HarpID].Priority)
	}
}

// TestLintTasks_FindsAndClearsViolations exercises operations.LintTasks
// end-to-end against a real store: a bad triage:type value is flagged, and
// an all-clean store reports nothing.
//
// The bad value is written DIRECTLY through the underlying tasks.Store,
// bypassing validateTag's write-seam gate (see
// TestAddTaskWithTagsRejectsDeclaredEnumViolation, which pins that
// AddTaskWithTags itself now rejects this exact value outright) — this
// simulates data written before the gate existed, or by a foreign writer
// this schema never saw, which is exactly the pre-existing data lint's
// read-time, advisory-only sweep exists to catch (see
// internal/shared/tasks/lint's package doc).
func TestLintTasks_FindsAndClearsViolations(t *testing.T) {
	taskstest.Isolate(t)
	schema, err := tagschema.Parse([]string{
		`tagma.enum:"triage:type"="correctness,security"`,
	})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: schema}

	logPath, err := paths.HomeTasksLogPath("p")
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	store, err := tasks.OpenLog(logPath, "sess")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := store.AddWithTags("bad", "", "", "triage:type=nonsense"); err != nil {
		t.Fatalf("add (bypassing the write-seam gate): %v", err)
	}

	result, err := LintTasks(tc)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if len(result.Violations) != 1 {
		t.Fatalf("violations = %+v, want exactly one", result.Violations)
	}

	tc2 := TaskContext{WorkDir: t.TempDir(), ProjectID: "clean", SessionHarp: "sess", TagSchema: schema}
	if _, err := AddTaskWithTags(tc2, "good", "", "", []string{"triage:type=security"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	result, err = LintTasks(tc2)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if len(result.Violations) != 0 {
		t.Fatalf("violations = %+v, want none for clean data", result.Violations)
	}
}

// TestTaskResult_PopulatesEveryField pins the one constructor every
// single-task mutation returns through. An unset field here is a field the
// frontend silently renders as empty on every mutation path at once.
func TestTaskResult_PopulatesEveryField(t *testing.T) {
	store, err := tasks.OpenLog(filepath.Join(t.TempDir(), "p.jsonl"), "swift-amber-falcon")
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}

	proj := projectIdentity{ID: "proj-id", Dir: "/proj/dir"}
	task := tasks.Task{HarpID: "aa-bb-cc", Text: "t"}
	got := proj.taskResult(store, task, "a warning")

	if got.Path != store.Path() || got.Task.HarpID != task.HarpID ||
		got.Warning != "a warning" || got.ProjectID != "proj-id" || got.ProjectDir != "/proj/dir" {
		t.Fatalf("taskResult mispopulated: %+v", got)
	}
	v := reflect.ValueOf(*got)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("TaskResult.%s left at its zero value", v.Type().Field(i).Name)
		}
	}
}

// TestScalarCollapse_SameValueSpelledTwoWaysIsNotARetraction pins the fix:
// scalar collapse decides TARGET identity by parsing the tag through tagma,
// but decided VALUE identity by comparing raw strings. tagma's grammar admits
// a quoted spelling of any component, so `triage:type=bug` and
// `triage:type="bug"` are one and the same tag — same target, same value — yet
// the raw comparison saw two different strings and emitted a retraction plus a
// re-tag for a no-op re-tagging. That writes a permanent untag into an
// append-only log for a value that never changed, and momentarily leaves the
// task carrying two spellings of one scalar.
func TestScalarCollapse_SameValueSpelledTwoWaysIsNotARetraction(t *testing.T) {
	schema := scalarSchema(t)

	toAdd, toUntag := scalarCollapse(schema, []string{"triage:type=bug"}, []string{`triage:type="bug"`})
	if len(toUntag) != 0 {
		t.Fatalf("toUntag = %v; the incoming tag is the SAME value, quoted — no retraction is due", toUntag)
	}
	if len(toAdd) != 1 {
		t.Fatalf("toAdd = %v, want the incoming tag passed through", toAdd)
	}

	// The genuine change must still retract, or the fix would have disarmed
	// the collapse entirely.
	toAdd, toUntag = scalarCollapse(schema, []string{"triage:type=bug"}, []string{`triage:type="security"`})
	if len(toUntag) != 1 || toUntag[0] != "triage:type=bug" {
		t.Fatalf("toUntag = %v, want the superseded value retracted", toUntag)
	}
	if len(toAdd) != 1 {
		t.Fatalf("toAdd = %v", toAdd)
	}
}

// TestScalarCollapse_TagTaskDoesNotRewriteHistoryForARespelledValue is the
// end-to-end half of the fix: re-tagging with the same value spelled
// differently must not append an untag event to the log.
func TestScalarCollapse_TagTaskDoesNotRewriteHistoryForARespelledValue(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: scalarSchema(t)}

	add, err := AddTaskWithTags(tc, "triage this", "", "", []string{"triage:type=bug"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := TagTask(tc, add.Task.HarpID, []string{`triage:type="bug"`}, nil); err != nil {
		t.Fatalf("tag: %v", err)
	}
	ops := make([]string, 0, 4)
	for _, ev := range readTaskEvents(t, tc, add.Task.HarpID) {
		ops = append(ops, ev.Op)
	}
	if slices.Contains(ops, "untag") {
		t.Fatalf("log ops = %v; a re-tag of the same value must not retract it", ops)
	}
}

// TestTagTask_NeverReturnsAnEmptyTaskWithANilError refutes the claim that
// the `if len(addTags) > 0` guard leaves TagTask a path to return
// TaskResult{Task: tasks.Task{}} with a nil error. It cannot: scalarCollapse
// keeps the LAST occurrence of every scalar target and passes every
// non-scalar tag through, so a non-empty incoming list can never collapse to
// an empty one, and TagTask rejects an all-empty call before it gets there.
//
// The guard is therefore unreachable rather than wrong, and what actually
// holds the property up is scalarCollapse's survivor rule — so that is what
// this pins. If a future collapse ever CAN empty a non-empty list, this goes
// red and the guard becomes a real silent-success path.
func TestTagTask_NeverReturnsAnEmptyTaskWithANilError(t *testing.T) {
	schema := scalarSchema(t)
	for _, incoming := range [][]string{
		{"triage:type=bug"},
		{"triage:type=bug", "triage:type=security"},
		{"triage:type=bug", "triage:type=bug"},
		{"urgent"},
		{"urgent", "triage:type=bug", "triage:type=security"},
	} {
		toAdd, _ := scalarCollapse(schema, []string{"triage:type=old"}, incoming)
		if len(toAdd) == 0 {
			t.Fatalf("scalarCollapse(%v) returned an empty add list; TagTask's len(addTags) > 0 guard would then report success with an empty task", incoming)
		}
	}

	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "p", SessionHarp: "sess", TagSchema: schema}
	add, err := AddTaskWithTags(tc, "triage this", "", "", []string{"triage:type=bug"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := TagTask(tc, add.Task.HarpID, []string{"triage:type=security"}, nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	if got.Task.HarpID == "" {
		t.Fatalf("TagTask returned an empty task with a nil error: %+v", got)
	}
	if !slices.Contains(got.Task.Tags, "triage:type=security") {
		t.Fatalf("Tags = %v, want the collapsed-to value", got.Task.Tags)
	}
}

// writeHostileMarker plants an in-tree project marker whose value
// projectid.ValidateProjectID rejects — the case ReadMarker exists to catch,
// since the marker travels with the working tree and can be committed by a
// third party.
func writeHostileMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ctxloom", "project-id"), []byte("../../etc/passwd\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

// TestResolveProjectIdentity_NamesTheStageThatFailed pins the fix: this
// function returned both of its failure modes bare, so its callers
// (internal/cli/run.go's pre-launch export and coord_host.go's state-dir key)
// could not tell "the project registry would not open" from "this tree's
// identity could not be resolved" — two different operator actions. Every
// other resolver in this file already wraps; this one now does too.
func TestResolveProjectIdentity_NamesTheStageThatFailed(t *testing.T) {
	taskstest.Isolate(t)
	proj := t.TempDir()
	writeHostileMarker(t, proj)

	_, _, err := ResolveProjectIdentity(proj)
	if err == nil {
		t.Fatal("a marker that fails validation must not resolve")
	}
	if !strings.Contains(err.Error(), "resolve project id") {
		t.Fatalf("error %q does not name the stage that failed", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid project marker") {
		t.Fatalf("error %q lost the underlying cause", err.Error())
	}
}

// TestResolveTaskStore_SaysWhenTheCwdMarkerCouldNotBeRead pins the fix. The
// pin-vs-cwd mismatch check reads the working directory's own marker with
// `cwdID, _ := projectid.ReadMarker(...)`, discarding the error from the one
// function written specifically to REJECT a hostile marker. A rejected marker
// then looks exactly like no marker at all: the check silently falls through
// to the registry and, when that has nothing either, says nothing — so a
// tree carrying a marker crafted to escape the tasks directory produces the
// same silence as a perfectly ordinary tree.
func TestResolveTaskStore_SaysWhenTheCwdMarkerCouldNotBeRead(t *testing.T) {
	taskstest.Isolate(t)
	work := t.TempDir()
	writeHostileMarker(t, work)
	tc := TaskContext{WorkDir: work, ProjectID: "pinned-project", SessionHarp: "sess"}

	got, err := AddTask(tc, "a task", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(got.Warning, "marker") {
		t.Fatalf("warning = %q; a marker ReadMarker refused must be reported, not silently treated as absent", got.Warning)
	}
	if got.ProjectID != "pinned-project" {
		t.Fatalf("project = %q; the pin must still win — this is a diagnostic, not a refusal", got.ProjectID)
	}
}

// writeRegistry plants a raw project-registry file, so a test can express
// registry states the API would refuse to create — the registry is a
// user-editable YAML file, so these states are reachable in the field.
func writeRegistry(t *testing.T, body string) {
	t.Helper()
	path, err := paths.ProjectRegistryPath()
	if err != nil {
		t.Fatalf("registry path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

// TestMissingLogSiblingNote_SaysWhenItCouldNotLook pins the fix: the note
// swallowed a registry read failure and an unusable sibling id into the same
// empty string it returns for "nothing to report". The whole purpose of this
// note is to stop a project's real backlog hiding behind an honest-looking
// "(no tasks)", so answering "nothing hidden" when it never managed to look
// is the one answer it must not give.
func TestMissingLogSiblingNote_SaysWhenItCouldNotLook(t *testing.T) {
	t.Run("registry unreadable", func(t *testing.T) {
		taskstest.Isolate(t)
		work := t.TempDir()
		writeRegistry(t, "projects: [this is not a registry\n")
		tc := TaskContext{WorkDir: work, ProjectID: "pinned-project", SessionHarp: "sess"}

		got, err := ListTasks(tc, ListOptions{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(got.Warning, "could not") {
			t.Fatalf("warning = %q; an unreadable registry must not read as 'nothing to report'", got.Warning)
		}
	})

	t.Run("sibling id unusable", func(t *testing.T) {
		taskstest.Isolate(t)
		work := t.TempDir()
		writeRegistry(t, "projects:\n  - project_id: \"../escape\"\n    path: "+work+"\n")
		tc := TaskContext{WorkDir: work, ProjectID: "pinned-project", SessionHarp: "sess"}

		got, err := ListTasks(tc, ListOptions{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(got.Warning, "../escape") {
			t.Fatalf("warning = %q; a registry entry whose id has no usable log path must be named", got.Warning)
		}
	})
}
