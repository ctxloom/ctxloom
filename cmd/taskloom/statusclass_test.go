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

// seedOneOfEachStatus creates one task per status in the live taxonomy, with
// the task's text naming that status so a listing can be asserted on by
// substring. Deferred needs a trigger, so every task gets one.
func seedOneOfEachStatus(t *testing.T) {
	t.Helper()
	for _, s := range tasks.Statuses() {
		_, err := operations.AddTaskWithTags(mustTaskContext(t), "task in status "+s.Name, s.Name, "some condition", nil)
		require.NoError(t, err)
	}
}

// TestRunListCmd_StatusClassOpen pins `--status @open` end to end: it selects
// every non-terminal status — Deferred included, which the default active-only
// view hides and which is precisely the set a session could not spell in one
// call before — and excludes every terminal one.
func TestRunListCmd_StatusClassOpen(t *testing.T) {
	taskstest.ProjectDir(t)
	seedOneOfEachStatus(t)

	var stdout, stderr strings.Builder
	err := runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{
		Statuses: []string{tasks.StatusClassOpen},
		Format:   clifmt.FormatText,
	})
	require.NoError(t, err)

	for _, s := range tasks.Statuses() {
		line := "task in status " + s.Name
		if s.Terminal {
			assert.NotContains(t, stdout.String(), line, "@open must exclude the terminal status %q", s.Name)
			continue
		}
		assert.Contains(t, stdout.String(), line, "@open must include the non-terminal status %q", s.Name)
	}
}

// TestRunListCmd_StatusClassTerminal is @open's complement, and also pins
// that a class opts a listing out of the default active-only view exactly as
// a literal --status does: naming @terminal shows the completed tasks it
// asked for rather than an empty listing.
func TestRunListCmd_StatusClassTerminal(t *testing.T) {
	taskstest.ProjectDir(t)
	seedOneOfEachStatus(t)

	var stdout, stderr strings.Builder
	err := runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{
		Statuses: []string{tasks.StatusClassTerminal},
		Format:   clifmt.FormatText,
	})
	require.NoError(t, err)

	for _, s := range tasks.Statuses() {
		line := "task in status " + s.Name
		if s.Terminal {
			assert.Contains(t, stdout.String(), line, "@terminal must include %q", s.Name)
			continue
		}
		assert.NotContains(t, stdout.String(), line, "@terminal must exclude %q", s.Name)
	}
}

// TestRunListCmd_StatusClassMixesWithLiterals pins the mixing property: a
// class and a literal in one repeatable --status filter yield the union.
func TestRunListCmd_StatusClassMixesWithLiterals(t *testing.T) {
	taskstest.ProjectDir(t)
	seedOneOfEachStatus(t)

	var stdout, stderr strings.Builder
	err := runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{
		Statuses: []string{tasks.StatusClassTerminal, tasks.StatusInProgress},
		Format:   clifmt.FormatText,
	})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "task in status "+tasks.StatusDone)
	assert.Contains(t, stdout.String(), "task in status "+tasks.StatusArchived)
	assert.Contains(t, stdout.String(), "task in status "+tasks.StatusInProgress)
	assert.NotContains(t, stdout.String(), "task in status "+tasks.StatusToDo)
}

// TestRunListCmd_UnknownStatusClassFailsLoud pins that a typo'd class is an
// error, not a filter on a literal status nobody has — which would print an
// empty listing and read as "no such work".
func TestRunListCmd_UnknownStatusClassFailsLoud(t *testing.T) {
	taskstest.ProjectDir(t)
	seedOneOfEachStatus(t)

	var stdout, stderr strings.Builder
	err := runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{
		Statuses: []string{"@opne"},
		Format:   clifmt.FormatText,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@opne")
	assert.Empty(t, stdout.String(), "a rejected filter must print no listing at all")
}

// TestRunListCmd_StatusClassAppliesGlobally pins that the expansion sits at
// the ONE decision point both scopes pass through: --global honors a class
// too, rather than the sugar working only in the single-project path.
func TestRunListCmd_StatusClassAppliesGlobally(t *testing.T) {
	taskstest.ProjectDir(t)
	_, err := operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere deferred task", tasks.StatusDeferred, "some condition")
	require.NoError(t, err)
	_, err = operations.AddTask(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere done task", tasks.StatusDone, "")
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, mustTaskContext(t), listOptions{
		Statuses: []string{tasks.StatusClassOpen},
		Global:   true,
		Format:   clifmt.FormatText,
	})
	require.NoError(t, err)

	assert.Contains(t, stdout.String(), "elsewhere deferred task")
	assert.NotContains(t, stdout.String(), "elsewhere done task")
}
