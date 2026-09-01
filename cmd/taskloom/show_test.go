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

// TestRunShow_OneIDIsAnObjectSeveralAreAList pins the project's serialization
// rule at this command: a GROUP serializes as a JSON LIST, a SINGLE value as an
// OBJECT.
//
// This REVERSES the shape recorded for unruffled-sandbox, which made every
// response an array so a consumer never had to branch on the count. That
// uniformity was bought at the price of a single-subject command answering with
// a collection, and it made the documented idiom (`jq -r '.text'`) false for the
// one call everybody actually makes.
//
// Both halves are asserted, and the single case is asserted NEGATIVELY as well:
// unmarshalling one id into a []Task must FAIL. Without that, an implementation
// that kept emitting arrays would still satisfy an object-shaped assertion in
// any decoder lenient about it.
func TestRunShow_OneIDIsAnObjectSeveralAreAList(t *testing.T) {
	taskstest.ProjectDir(t)
	ids := addShowFixture(t, "solo body text", "second body text")

	t.Run("one id is a bare object", func(t *testing.T) {
		out, err := showForTest(t, "json", ids[0])
		require.NoError(t, err)

		var got tasks.Task
		require.NoError(t, json.Unmarshal([]byte(out), &got),
			"a single id must unmarshal as an OBJECT so `jq -r '.text'` reads its body: %s", out)
		require.Equal(t, ids[0], got.HarpID)
		require.Equal(t, "solo body text", got.Text)

		var asList []tasks.Task
		require.Error(t, json.Unmarshal([]byte(out), &asList),
			"a single id must NOT still be a one-element list; without this the object assertion above is satisfied by the old shape too")
	})

	t.Run("several ids are a list in argument order", func(t *testing.T) {
		out, err := showForTest(t, "json", ids[1], ids[0])
		require.NoError(t, err)
		var got []tasks.Task
		require.NoError(t, json.Unmarshal([]byte(out), &got),
			"two ids are a GROUP and must unmarshal as a list: %s", out)
		require.Len(t, got, 2)
		require.Equal(t, []string{ids[1], ids[0]}, []string{got[0].HarpID, got[1].HarpID})
	})

	// A repeated id is TWO ids asked for. The shape follows the request, not
	// the distinct count, so this stays a list — matching selectTasks, which
	// deliberately honors a repeat rather than de-duplicating it.
	t.Run("a repeated id is still a group", func(t *testing.T) {
		out, err := showForTest(t, "json", ids[0], ids[0])
		require.NoError(t, err)
		var got []tasks.Task
		require.NoError(t, json.Unmarshal([]byte(out), &got), "output was: %s", out)
		require.Len(t, got, 2)
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
