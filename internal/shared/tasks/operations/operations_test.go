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
