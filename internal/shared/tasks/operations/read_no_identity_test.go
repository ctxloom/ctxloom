package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// readsEverything runs every task READ for tc, requiring each to succeed and
// come back empty.
func readsEverything(t *testing.T, tc TaskContext) *TaskListResult {
	t.Helper()
	list, err := ListTasks(tc, ListOptions{IncludeDone: true})
	require.NoError(t, err)
	require.Empty(t, list.Tasks)
	tags, err := ListTagCounts(tc)
	require.NoError(t, err)
	require.Empty(t, tags.Tags)
	since, err := DeferredSince(tc)
	require.NoError(t, err)
	require.Empty(t, since)
	prio, _, err := ComputeTaskPriorities(tc, time.Now())
	require.NoError(t, err)
	require.Empty(t, prio)
	lr, err := LintTasks(tc)
	require.NoError(t, err)
	require.Empty(t, lr.Violations)
	return list
}

// A read in a directory that has no project yet answers "no tasks" and
// conjures nothing: no in-tree marker, no registry, no task directory. The
// first WRITE mints the identity, and reads see it from then on.
func TestTaskReads_NoProjectYetWriteNothingAndTheFirstWriteMints(t *testing.T) {
	home := taskstest.Isolate(t)
	proj := t.TempDir()
	tc := TaskContext{WorkDir: proj}

	list := readsEverything(t, tc)
	require.Empty(t, list.ProjectID)
	require.Equal(t, fmt.Sprintf(noProjectYetNote, proj), list.Warning)
	marker, err := projectid.ReadMarker(proj)
	require.NoError(t, err)
	require.Empty(t, marker, "a read must not mint a project marker")
	require.NoDirExists(t, filepath.Join(home, ".ctxloom"), "a read must not create the registry or the task store")

	add, err := AddTask(tc, "first", "", "")
	require.NoError(t, err)
	minted, err := projectid.ReadMarker(proj)
	require.NoError(t, err)
	require.NotEmpty(t, minted, "the first write mints")
	require.Equal(t, minted, add.ProjectID)

	after, err := ListTasks(tc, ListOptions{})
	require.NoError(t, err)
	require.Equal(t, minted, after.ProjectID)
	require.Len(t, after.Tasks, 1)
	require.Empty(t, after.Warning)
}

// A copied tree carries the original's marker. A read there must not show
// the original's tasks or fork the copy; the copy's first write forks.
func TestTaskReads_CopiedTreeReadsEmptyAndTheFirstWriteForks(t *testing.T) {
	taskstest.Isolate(t)
	orig := t.TempDir()
	_, err := AddTask(TaskContext{WorkDir: orig}, "the original's task", "", "")
	require.NoError(t, err)
	origID, err := projectid.ReadMarker(orig)
	require.NoError(t, err)

	cp := t.TempDir()
	require.NoError(t, projectid.WriteMarker(cp, origID))
	reg, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ctxloom", "projects", "index.yaml"))
	require.NoError(t, err)

	list := readsEverything(t, TaskContext{WorkDir: cp})
	require.Empty(t, list.ProjectID)
	still, err := projectid.ReadMarker(cp)
	require.NoError(t, err)
	require.Equal(t, origID, still, "a read must not fork the copy")
	regAfter, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ctxloom", "projects", "index.yaml"))
	require.NoError(t, err)
	require.Equal(t, string(reg), string(regAfter), "a read must not touch the registry")

	add, err := AddTask(TaskContext{WorkDir: cp}, "the copy's task", "", "")
	require.NoError(t, err)
	require.NotEqual(t, origID, add.ProjectID, "the copy's first write forks a fresh identity")
}
