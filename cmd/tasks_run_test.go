package cmd

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/tasks"
)

// captureRunArgs swaps the execCommand seam for a recorder that returns a
// harmless /bin/true, runs fn, and returns the captured argv.
func captureRunArgs(t *testing.T, fn func() error) []string {
	t.Helper()
	var captured []string
	orig := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return exec.Command("/bin/true")
	}
	t.Cleanup(func() { execCommand = orig })
	require.NoError(t, fn())
	return captured
}

func loc(harpID, text, status, origin string) tasks.Located {
	return tasks.Located{
		Task:       tasks.Task{HarpID: harpID, Text: text, Status: status},
		OriginHarp: origin,
	}
}

func TestLaunchTaskAgent_SessionOriginContinues(t *testing.T) {
	chosen := loc("swift-amber-falcon", "wire up X", tasks.StatusToDo, "origin-harp")
	args := captureRunArgs(t, func() error { return launchTaskAgent(chosen, false) })

	require.NotEmpty(t, args)
	assert.Equal(t, "run", args[1], "first arg after exe must be the run subcommand")
	joined := strings.Join(args, " ")
	// Continuation of the origin session + single-task seed, not whole-store carry.
	assert.Contains(t, joined, "--session origin-harp")
	assert.Contains(t, joined, "--no-tasks")
	assert.Contains(t, joined, "--seed-task swift-amber-falcon")
	// Default launch marks In Progress, so no explicit --seed-status override.
	assert.NotContains(t, joined, "--seed-status")
	// Prompt is the wrapped task text.
	assert.Contains(t, args, "Work on this task (`swift-amber-falcon`): wire up X")
}

func TestLaunchTaskAgent_LegacyOriginNoSession(t *testing.T) {
	chosen := loc("quiet-silver-meadow", "fix Y", tasks.StatusToDo, "") // legacy store
	args := captureRunArgs(t, func() error { return launchTaskAgent(chosen, false) })

	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "--session", "legacy-origin tasks have no session to continue")
	assert.NotContains(t, joined, "--no-tasks")
	assert.Contains(t, joined, "--seed-task quiet-silver-meadow")
	assert.Contains(t, args, "Work on this task (`quiet-silver-meadow`): fix Y")
}

func TestLaunchTaskAgent_NoStartPassesToDoStatus(t *testing.T) {
	chosen := loc("misty-golden-river", "later task", tasks.StatusToDo, "origin-harp")
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

func TestPickTask_SelectsFromDefaultOpenView(t *testing.T) {
	all := []tasks.Located{
		loc("a", "open todo", tasks.StatusToDo, "h1"),
		loc("b", "done thing", tasks.StatusDone, "h1"), // hidden by default
		loc("c", "in progress", tasks.StatusInProgress, "h2"),
	}
	// Row 2 of the default (open-only) view is the In Progress task, not the Done one.
	got, ok := pickTask(&bytes.Buffer{}, strings.NewReader("2\n"), all)
	require.True(t, ok)
	assert.Equal(t, "c", got.HarpID, "default view must skip Done; row 2 = In Progress task")
}

func TestPickTask_ToggleAllRevealsClosed(t *testing.T) {
	all := []tasks.Located{
		loc("a", "open todo", tasks.StatusToDo, "h1"),
		loc("b", "done thing", tasks.StatusDone, "h1"),
	}
	// "a" reveals all statuses, then row 2 is the Done task.
	got, ok := pickTask(&bytes.Buffer{}, strings.NewReader("a\n2\n"), all)
	require.True(t, ok)
	assert.Equal(t, "b", got.HarpID)
}

func TestPickTask_QuitAndEmpty(t *testing.T) {
	all := []tasks.Located{loc("a", "open todo", tasks.StatusToDo, "h1")}
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

func TestFindLocated(t *testing.T) {
	all := []tasks.Located{
		loc("a", "one", tasks.StatusToDo, "h1"),
		loc("b", "two", tasks.StatusDone, "h2"),
	}
	got, ok := findLocated(all, "b")
	require.True(t, ok)
	assert.Equal(t, "two", got.Text)
	if _, ok := findLocated(all, "z"); ok {
		t.Error("missing id must report not found")
	}
}
