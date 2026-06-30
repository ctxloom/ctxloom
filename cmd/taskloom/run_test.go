package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

// captureRunArgs swaps the execCommand seam for a recorder that returns a
// harmless /bin/true, runs fn, and returns the captured argv. The lookPath
// seam is stubbed to succeed so the tests stay hermetic — they must not
// depend on a real ctxloom being installed on PATH.
func captureRunArgs(t *testing.T, fn func() error) []string {
	t.Helper()
	var captured []string
	origExec, origLook := execCommand, lookPath
	execCommand = func(name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return exec.Command("/bin/true")
	}
	lookPath = func(file string) (string, error) { return "/stub/" + file, nil }
	t.Cleanup(func() {
		execCommand = origExec
		lookPath = origLook
	})
	require.NoError(t, fn())
	return captured
}

func task(harpID, text, status, origin string) tasks.Task {
	return tasks.Task{HarpID: harpID, Text: text, Status: status, OriginSession: origin}
}

func TestLaunchTaskAgent_SessionOriginContinues(t *testing.T) {
	chosen := task("swift-amber-falcon", "wire up X", tasks.StatusToDo, "origin-harp")
	args := captureRunArgs(t, func() error { return launchTaskAgent(chosen, false) })

	require.NotEmpty(t, args)
	assert.Equal(t, "ctxloom", args[0], "launching shells out to ctxloom on PATH")
	assert.Equal(t, "run", args[1], "first arg after exe must be the run subcommand")
	joined := strings.Join(args, " ")
	// Continuation of the origin session + single-task seed.
	assert.Contains(t, joined, "--session origin-harp")
	assert.Contains(t, joined, "--seed-task swift-amber-falcon")
	// Default launch marks In Progress, so no explicit --seed-status override.
	assert.NotContains(t, joined, "--seed-status")
	// Prompt is the wrapped task text.
	assert.Contains(t, args, "Work on this task (`swift-amber-falcon`): wire up X")
}

func TestLaunchTaskAgent_NoOriginNoSession(t *testing.T) {
	chosen := task("quiet-silver-meadow", "fix Y", tasks.StatusToDo, "")
	args := captureRunArgs(t, func() error { return launchTaskAgent(chosen, false) })

	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "--session", "origin-less tasks have no session to continue")
	assert.Contains(t, joined, "--seed-task quiet-silver-meadow")
	assert.Contains(t, args, "Work on this task (`quiet-silver-meadow`): fix Y")
}

func TestLaunchTaskAgent_NoStartPassesToDoStatus(t *testing.T) {
	chosen := task("misty-golden-river", "later task", tasks.StatusToDo, "origin-harp")
	args := captureRunArgs(t, func() error { return launchTaskAgent(chosen, true) })

	// argv carries the pair "--seed-status" "To Do".
	var sawPair bool
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--seed-status" && args[i+1] == tasks.StatusToDo {
			sawPair = true
		}
	}
	assert.True(t, sawPair, "--no-start must pass --seed-status \"To Do\"; got %v", args)
}

func TestLaunchTaskAgent_CtxloomMissing_ClearError(t *testing.T) {
	// taskloom is standalone; `run` is its only ctxloom-coupled subcommand.
	// With ctxloom absent from PATH the failure must name the dependency and
	// what still works, not leak a bare exec error.
	t.Setenv("PATH", t.TempDir())
	chosen := task("swift-amber-falcon", "wire up X", tasks.StatusToDo, "")
	err := launchTaskAgent(chosen, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires ctxloom on PATH")
	assert.NotContains(t, err.Error(), "executable file not found")
}

func TestPickTask_SelectsFromDefaultOpenView(t *testing.T) {
	all := []tasks.Task{
		task("a", "open todo", tasks.StatusToDo, "h1"),
		task("b", "done thing", tasks.StatusDone, "h1"), // hidden by default
		task("c", "in progress", tasks.StatusInProgress, "h2"),
	}
	// Row 2 of the default (open-only) view is the In Progress task, not the Done one.
	got, ok := pickTask(&bytes.Buffer{}, strings.NewReader("2\n"), all)
	require.True(t, ok)
	assert.Equal(t, "c", got.HarpID, "default view must skip Done; row 2 = In Progress task")
}

func TestPickTask_ToggleAllRevealsClosed(t *testing.T) {
	all := []tasks.Task{
		task("a", "open todo", tasks.StatusToDo, "h1"),
		task("b", "done thing", tasks.StatusDone, "h1"),
	}
	// "a" reveals all statuses, then row 2 is the Done task.
	got, ok := pickTask(&bytes.Buffer{}, strings.NewReader("a\n2\n"), all)
	require.True(t, ok)
	assert.Equal(t, "b", got.HarpID)
}

func TestPickTask_QuitAndEmpty(t *testing.T) {
	all := []tasks.Task{task("a", "open todo", tasks.StatusToDo, "h1")}
	if _, ok := pickTask(&bytes.Buffer{}, strings.NewReader("q\n"), all); ok {
		t.Error("q must cancel")
	}
	if _, ok := pickTask(&bytes.Buffer{}, strings.NewReader(""), all); ok {
		t.Error("EOF must cancel")
	}
	if _, ok := pickTask(&bytes.Buffer{}, strings.NewReader("1\n"), nil); ok {
		t.Error("no tasks must cancel")
	}
}

func TestFindTask(t *testing.T) {
	all := []tasks.Task{
		task("a", "one", tasks.StatusToDo, "h1"),
		task("b", "two", tasks.StatusDone, "h2"),
	}
	got, ok := findTask(all, "b")
	require.True(t, ok)
	assert.Equal(t, "two", got.Text)
	if _, ok := findTask(all, "z"); ok {
		t.Error("missing id must report not found")
	}
}
