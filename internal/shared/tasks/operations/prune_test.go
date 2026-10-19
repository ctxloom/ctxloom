package operations

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// A prune is a read before it is a write: in a directory with no project yet
// there is nothing to prune, and looking — even under Apply — must not mint
// the identity a first add would.
func TestPruneTasks_NoProjectYetPrunesNothingAndMintsNothing(t *testing.T) {
	home := taskstest.Isolate(t)
	proj := t.TempDir()

	res, err := PruneTasks(TaskContext{WorkDir: proj}, tasks.PruneRequest{
		Statuses: []string{tasks.StatusDone}, Before: time.Now(), Apply: true,
	})
	require.NoError(t, err)
	assert.Empty(t, res.Pruned)
	assert.NotEmpty(t, res.Warning, "the caller is told why there is nothing")
	marker, err := projectid.ReadMarker(proj)
	require.NoError(t, err)
	assert.Empty(t, marker, "a prune must not mint a project marker")
	assert.NoDirExists(t, filepath.Join(home, ".ctxloom"), "a prune must not create the registry or the task store")
}

// PruneTasks prunes the store tc resolves to, names it, and evaluates the tag
// query through the project's tag schema: a relational query over a
// semver-typed target matches nothing at all without it.
func TestPruneTasks_PrunesTheResolvedStoreAndNamesIt(t *testing.T) {
	taskstest.Isolate(t)
	tc := TaskContext{WorkDir: t.TempDir(), ProjectID: "prune-project", TagSchema: blocksReleaseSemverSchema(t)}
	done, err := AddTaskWithTags(tc, "finished", tasks.StatusDone, "", []string{"triage:blocks-release=0.7.0"})
	require.NoError(t, err)
	_, err = AddTaskWithTags(tc, "finished later", tasks.StatusDone, "", []string{"triage:blocks-release=0.8.0"})
	require.NoError(t, err)
	_, err = AddTask(tc, "still open", "", "")
	require.NoError(t, err)

	res, err := PruneTasks(tc, tasks.PruneRequest{
		Statuses: []string{tasks.StatusDone}, Before: time.Now().Add(time.Hour), TagQuery: "triage:blocks-release<=0.7.0", Apply: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "prune-project", res.ProjectID)
	assert.Equal(t, done.Path, res.Path)
	require.Len(t, res.Pruned, 1)
	assert.Equal(t, done.Task.HarpID, res.Pruned[0].HarpID)

	left, err := ListTasks(tc, ListOptions{IncludeDone: true})
	require.NoError(t, err)
	var texts []string
	for _, task := range left.Tasks {
		texts = append(texts, task.Text)
	}
	assert.ElementsMatch(t, []string{"finished later", "still open"}, texts)
}
