package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// TestRunRepairCmd_ReintroducesDisplacedTaskAndUnblocksList pins the shipped
// half of U120-F01/U007-F11: the fatal harp-collision error names
// `taskloom repair` as the remedy, but before this command existed, that
// remedy was unreachable from anywhere a user could actually act on it — a
// collision made list/lint/tag-query/priority all fail loud forever, with
// no shipped surface to fix it (only Store.Repair(), a Go method no CLI
// command called).
func TestRunRepairCmd_ReintroducesDisplacedTaskAndUnblocksList(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	_, logPath, err := operations.ResolveLogPath(tc)
	require.NoError(t, err)
	// Two adds independently claim the same harp -- the same shape a
	// concurrent-mint race or a union-merge across branches produces.
	raw := `{"op":"add","task":"alpha","text":"first writer","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"alpha","text":"displaced writer","status":"To Do","ts":"2026-01-01T00:00:01Z"}
`
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o755))
	require.NoError(t, os.WriteFile(logPath, []byte(raw), 0o644))

	// Pre-repair: every read fails loud, exactly the case `repair` exists for.
	_, listErr := operations.ListTasks(tc, nil, "", false, false, 0)
	require.Error(t, listErr, "precondition: an unresolved collision must fail every read")

	var out strings.Builder
	err = runRepairCmd(&out, tc)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "repair complete")

	res, err := operations.ListTasks(tc, nil, "", false, false, 0)
	require.NoError(t, err, "list must succeed after repair")
	require.Len(t, res.Tasks, 2)
	var texts []string
	for _, task := range res.Tasks {
		texts = append(texts, task.Text)
	}
	assert.Contains(t, texts, "first writer")
	assert.Contains(t, texts, "displaced writer")
}

// TestRunRepairCmd_NoOpOnCleanLog proves repair is safe to run defensively —
// a project with no unresolved collision is left untouched.
func TestRunRepairCmd_NoOpOnCleanLog(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	_, err = operations.AddTask(tc, "just one task", "", "")
	require.NoError(t, err)

	var out strings.Builder
	err = runRepairCmd(&out, tc)
	require.NoError(t, err)

	res, err := operations.ListTasks(tc, nil, "", false, false, 0)
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, "just one task", res.Tasks[0].Text)
}
