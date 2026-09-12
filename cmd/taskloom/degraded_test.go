package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// storeBytes reads the raw bytes of the current project's task log under the
// isolated HOME — the ground truth the strictness tests assert on, since a
// returned error says only what the caller was TOLD. A log never created reads
// as empty.
func storeBytes(t *testing.T) string {
	t.Helper()
	_, logPath, err := operations.ResolveLogPath(mustTaskContext(t))
	require.NoError(t, err)
	b, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

// executeAdd drives `taskloom add <text> --format text <args...>` through
// the real cobra tree — the persistent --degraded flag included — against
// the isolated store, returning the error and everything the command wrote
// to its out/err streams. The format is pinned to text because a
// non-terminal stdout otherwise resolves to JSON (cliemit.Resolve), which
// would turn the diagnostic lines these tests read into JSON envelopes. The package-global rootCmd is reset on cleanup exactly as
// executeFailingUnderFormat does, plus the --degraded flag this test family
// introduces (a Changed --degraded leaking into the next test would turn its
// strict-mode assertion into a false pass).
func executeAdd(t *testing.T, text string, args ...string) (string, error) {
	t.Helper()
	var buf strings.Builder
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"add", text, "--format", "text"}, args...))
	resetGlobalFormatFlags()
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		resetGlobalFormatFlags()
		clidiag.SetStructured(false)
		for _, name := range []string{"degraded", "tag"} {
			f := rootCmd.PersistentFlags().Lookup(name)
			if f == nil {
				f = addCmd.Flags().Lookup(name)
			}
			require.NotNil(t, f, "flag %q must exist on the tree", name)
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
		tasksAddTags = nil
	})
	err := rootCmd.Execute()
	return buf.String(), err
}

// TestAdd_StrictRefusesSchemaRejectedTagAndWritesNothing pins the strict half
// of the row's SETTLES clause end to end: `taskloom add` with a tag the
// project's (default) tag_schema rejects refuses, names the tag, and leaves
// the store's bytes empty.
func TestAdd_StrictRefusesSchemaRejectedTagAndWritesNothing(t *testing.T) {
	taskstest.ProjectDir(t)
	diag := taskstest.Strictness(t, false)

	_, err := executeAdd(t, "strict add", "--tag", "urgent", "--tag", "triage:kind=sparkles")
	require.Error(t, err, "strict mode must refuse the add")
	assert.Contains(t, err.Error(), "triage:kind=sparkles")
	assert.Equal(t, "", storeBytes(t), "a strict refusal writes NOTHING")
	assert.Contains(t, diag.String(), progName+": warning:", "the refusal is printed under taskloom's own name")
	assert.Contains(t, diag.String(), "triage:kind=sparkles")
}

// TestAdd_DegradedCreatesRowWithoutRefusedTagAndPrintsRefusal pins the
// degraded half: `taskloom add --degraded` with the same tag creates the row
// WITHOUT that tag, keeps the admitted one, and prints the refusal — all
// asserted on the store's bytes.
func TestAdd_DegradedCreatesRowWithoutRefusedTagAndPrintsRefusal(t *testing.T) {
	taskstest.ProjectDir(t)
	diag := taskstest.Strictness(t, false) // the FLAG must flip the mode, not the test

	out, err := executeAdd(t, "degraded add", "--degraded", "--tag", "urgent", "--tag", "triage:kind=sparkles")
	require.NoError(t, err, "degraded mode must skip the refused tag, not refuse the add; out:\n%s", out)
	assert.True(t, strictness.Degraded(), "--degraded must switch the process into degraded mode")

	got := storeBytes(t)
	assert.Contains(t, got, "degraded add", "the row must land")
	assert.Contains(t, got, `"urgent"`, "the admitted tag must land")
	assert.NotContains(t, got, "sparkles", "degraded must NEVER write the refused tag")
	assert.Contains(t, diag.String(), "triage:kind=sparkles", "the refusal must be printed")
	assert.Contains(t, out, "degraded add", "the created row is still emitted on stdout")
}

// TestHandleTaskAdd_DegradedReportsRefusedTagsOnTheResult pins the MCP
// surface: the model driving task_add never sees the server's stderr, so a
// skipped tag has to come back on the result itself.
func TestHandleTaskAdd_DegradedReportsRefusedTagsOnTheResult(t *testing.T) {
	taskstest.ProjectDir(t)
	taskstest.Strictness(t, true)

	_, res, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "via mcp", Tags: []string{"urgent", "triage:kind=sparkles"}})
	require.NoError(t, err)
	require.Len(t, res.Refused, 1, "the skipped tag must be reported on the result")
	assert.Contains(t, res.Refused[0], "triage:kind=sparkles")
	assert.Equal(t, []string{"urgent"}, res.Task.Tags)
	assert.NotContains(t, storeBytes(t), "sparkles")
}

// TestHandleTaskTag_DegradedReportsRefusedTagsOnTheResult is the same pin on
// task_tag's add list.
func TestHandleTaskTag_DegradedReportsRefusedTagsOnTheResult(t *testing.T) {
	taskstest.ProjectDir(t)
	taskstest.Strictness(t, true)

	_, added, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "via mcp"})
	require.NoError(t, err)
	_, res, err := handleTaskTag(context.Background(), nil, taskTagInput{HarpID: added.Task.HarpID, Add: []string{"urgent", "triage:kind=sparkles"}})
	require.NoError(t, err)
	require.Len(t, res.Refused, 1)
	assert.Contains(t, res.Refused[0], "triage:kind=sparkles")
	assert.Equal(t, []string{"urgent"}, res.Task.Tags)
	assert.NotContains(t, storeBytes(t), "sparkles")
}
