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
	cmd.Flags().String("format", "", "")
	// Set, not defaulted: cliemit.Resolve honours only an EXPLICIT --format
	// and otherwise derives one from stdout not being a terminal, which under
	// `go test` would turn every "text" call into JSON.
	require.NoError(t, cmd.Flags().Set("format", format))
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

// TestSelectRows covers the resolution helper directly: argument order, the
// full missing list rather than only the first, and a repeated id honored as
// typed rather than silently collapsed to one record.
func TestSelectRows(t *testing.T) {
	all := []taskRow{{Task: tasks.Task{HarpID: "a", Text: "A"}}, {Task: tasks.Task{HarpID: "b", Text: "B"}}}

	selected, missing, ambiguous := selectRows(all, []string{"b", "a"})
	require.Empty(t, missing)
	require.Empty(t, ambiguous)
	require.Equal(t, []string{"b", "a"}, []string{selected[0].HarpID, selected[1].HarpID})

	selected, missing, _ = selectRows(all, []string{"a", "x", "b", "y"})
	require.Equal(t, []string{"x", "y"}, missing, "every unresolved id must be reported, not just the first")
	require.Len(t, selected, 2)

	selected, missing, _ = selectRows(all, []string{"a", "a"})
	require.Empty(t, missing)
	require.Len(t, selected, 2, "a repeated id yields a record per argument")
}

// TestSelectRows_HarpInTwoProjectsIsAmbiguous pins the --global hazard: harp
// ids are unique within one project's log, not across projects, so a harp
// found in two stores must fail naming both rather than silently showing
// whichever project sorted first.
func TestSelectRows_HarpInTwoProjectsIsAmbiguous(t *testing.T) {
	all := []taskRow{
		{Task: tasks.Task{HarpID: "dup", Text: "A's"}, ProjectID: "proj-a"},
		{Task: tasks.Task{HarpID: "dup", Text: "B's"}, ProjectID: "proj-b"},
		{Task: tasks.Task{HarpID: "solo", Text: "S"}, ProjectID: "proj-a"},
	}
	_, missing, ambiguous := selectRows(all, []string{"solo", "dup"})
	require.Empty(t, missing)
	require.Equal(t, []ambiguousHarp{{HarpID: "dup", ProjectIDs: []string{"proj-a", "proj-b"}}}, ambiguous)

	msg := ambiguousTasksError(ambiguous).Error()
	require.Contains(t, msg, `"dup"`)
	require.Contains(t, msg, "proj-a")
	require.Contains(t, msg, "proj-b")
	require.Contains(t, msg, "--project", "the error must name the way to disambiguate")
}

// TestRunShow_GlobalResolvesAcrossPrivatelyHomedProjects pins that `show`
// has the scope `list` has: with --global, a harp held by ANOTHER project's
// private store resolves, and the structured record names the project it
// came from.
func TestRunShow_GlobalResolvesAcrossPrivatelyHomedProjects(t *testing.T) {
	taskstest.Isolate(t)
	_, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-a"}, "a's body", "", "")
	require.NoError(t, err)
	b, err := operations.AddTask(operations.TaskContext{ProjectID: "proj-b"}, "b's body", "", "")
	require.NoError(t, err)

	setShowGlobal(t, true)
	out, err := showForTest(t, "json", b.Task.HarpID)
	require.NoError(t, err)

	var got taskRow
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output was: %s", out)
	require.Equal(t, b.Task.HarpID, got.HarpID)
	require.Equal(t, "b's body", got.Text)
	require.Equal(t, "proj-b", got.ProjectID)
}

// TestRunShow_WithoutGlobalStaysInTheResolvedProject pins the other half: the
// default scope is the resolved project, so a harp living only in some OTHER
// private project is "not found" unless --global widens the read.
func TestRunShow_WithoutGlobalStaysInTheResolvedProject(t *testing.T) {
	taskstest.ProjectDir(t)
	// A write establishes this directory as a project; without one, the read
	// would (correctly, as `list` does) fall back to every project.
	addShowFixture(t, "here")
	other, err := operations.AddTask(operations.TaskContext{ProjectID: "some-other-project"}, "elsewhere", "", "")
	require.NoError(t, err)

	setShowGlobal(t, false)
	_, err = showForTest(t, "text", other.Task.HarpID)
	require.Error(t, err)

	setShowGlobal(t, true)
	out, err := showForTest(t, "text", other.Task.HarpID)
	require.NoError(t, err)
	require.Contains(t, out, "elsewhere")
	require.Contains(t, out, "some-other-project", "a cross-project detail block must name its project")
}

// setShowGlobal sets the --global flag variable runShow reads for the
// duration of one test.
func setShowGlobal(t *testing.T, v bool) {
	t.Helper()
	prev := showGlobal
	showGlobal = v
	t.Cleanup(func() { showGlobal = prev })
}

// TestMissingTasksError pins the wording split: one id keeps the singular
// phrasing `show` has always used, several are all named in one message.
func TestMissingTasksError(t *testing.T) {
	require.Contains(t, missingTasksError([]string{"lone"}).Error(), `no task with harp id "lone"`)
	multi := missingTasksError([]string{"one", "two"}).Error()
	require.Contains(t, multi, `no tasks with harp ids "one", "two"`)
}
