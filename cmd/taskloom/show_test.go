package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// showForTest drives runShow the way cobra would, with --format carrying the
// requested value, and captures the command's stdout. It builds a standalone
// command rather than reusing rootCmd so tests never mutate the process-wide
// command tree.
func showForTest(t *testing.T, format string, args ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "show", RunE: runShow}
	cmd.Flags().String("format", format, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runShow(cmd, args)
	return out.String(), err
}

// addShowFixture creates n tasks whose text is distinctive per index, and
// returns their harp ids in creation order.
func addShowFixture(t *testing.T, texts ...string) []string {
	t.Helper()
	ids := make([]string, 0, len(texts))
	for _, text := range texts {
		res, err := operations.AddTaskWithTags(mustTaskContext(t), text, "", "", nil)
		require.NoError(t, err)
		ids = append(ids, res.Task.HarpID)
	}
	return ids
}

// TestRunShow_ManyIDsFollowArgumentOrder pins the variadic text view: every
// requested id renders a full detail block, and the blocks come back in the
// order they were TYPED, not the order the store happens to hold them in.
func TestRunShow_ManyIDsFollowArgumentOrder(t *testing.T) {
	taskstest.ProjectDir(t)
	ids := addShowFixture(t, "alpha body text", "bravo body text", "charlie body text")

	out, err := showForTest(t, "text", ids[2], ids[0], ids[1])
	require.NoError(t, err)

	for _, want := range []string{"alpha body text", "bravo body text", "charlie body text"} {
		require.Contains(t, out, want)
	}
	// Argument order, not creation order: charlie's block must precede
	// alpha's, which must precede bravo's.
	require.Less(t, strings.Index(out, "charlie body text"), strings.Index(out, "alpha body text"))
	require.Less(t, strings.Index(out, "alpha body text"), strings.Index(out, "bravo body text"))
	// Each id heads its own block.
	for _, id := range ids {
		require.Contains(t, out, id)
	}
}

// TestRunShow_JSONIsAlwaysAnArray pins the shape decision recorded for
// unruffled-sandbox: --format json emits an ARRAY of full task records for
// ANY number of ids, one included, so a consumer never branches on the count.
func TestRunShow_JSONIsAlwaysAnArray(t *testing.T) {
	taskstest.ProjectDir(t)
	ids := addShowFixture(t, "solo body text", "second body text")

	t.Run("one id", func(t *testing.T) {
		out, err := showForTest(t, "json", ids[0])
		require.NoError(t, err)
		var got []tasks.Task
		require.NoError(t, json.Unmarshal([]byte(out), &got), "single-id --format json must unmarshal as an array: %s", out)
		require.Len(t, got, 1)
		require.Equal(t, ids[0], got[0].HarpID)
		require.Equal(t, "solo body text", got[0].Text)
	})

	t.Run("several ids in argument order", func(t *testing.T) {
		out, err := showForTest(t, "json", ids[1], ids[0])
		require.NoError(t, err)
		var got []tasks.Task
		require.NoError(t, json.Unmarshal([]byte(out), &got))
		require.Len(t, got, 2)
		require.Equal(t, []string{ids[1], ids[0]}, []string{got[0].HarpID, got[1].HarpID})
	})
}

// TestRunShow_UnknownIDsFailLoudly pins the anti-silent-no-op contract: an
// unknown id fails the whole call, names EVERY id that did not resolve, and
// emits nothing at all — never the subset that happened to resolve, which
// would read as a complete answer.
func TestRunShow_UnknownIDsFailLoudly(t *testing.T) {
	taskstest.ProjectDir(t)
	ids := addShowFixture(t, "real body text")

	out, err := showForTest(t, "text", ids[0], "no-such-one", "no-such-two")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no-such-one")
	require.Contains(t, err.Error(), "no-such-two")
	require.Empty(t, out, "a partial result must not be emitted alongside the failure")
	require.NotContains(t, out, "real body text")
}

// TestSelectTasks covers the resolution helper directly: argument order, the
// full missing list rather than only the first, and a repeated id honored as
// typed rather than silently collapsed to one record.
func TestSelectTasks(t *testing.T) {
	all := []tasks.Task{{HarpID: "a", Text: "A"}, {HarpID: "b", Text: "B"}}

	selected, missing := selectTasks(all, []string{"b", "a"})
	require.Empty(t, missing)
	require.Equal(t, []string{"b", "a"}, []string{selected[0].HarpID, selected[1].HarpID})

	selected, missing = selectTasks(all, []string{"a", "x", "b", "y"})
	require.Equal(t, []string{"x", "y"}, missing, "every unresolved id must be reported, not just the first")
	require.Len(t, selected, 2)

	selected, missing = selectTasks(all, []string{"a", "a"})
	require.Empty(t, missing)
	require.Len(t, selected, 2, "a repeated id yields a record per argument")
}

// TestMissingTasksError pins the wording split: one id keeps the singular
// phrasing `show` has always used, several are all named in one message.
func TestMissingTasksError(t *testing.T) {
	require.Contains(t, missingTasksError([]string{"lone"}).Error(), `no task with harp id "lone"`)
	multi := missingTasksError([]string{"one", "two"}).Error()
	require.Contains(t, multi, `no tasks with harp ids "one", "two"`)
}
