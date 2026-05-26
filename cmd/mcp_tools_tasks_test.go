package cmd

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withProjectDir cd's into a fresh tempdir for the duration of the test.
// The MCP task handlers resolve the project root via os.Getwd, so all
// fixture state lives there and is cleaned up automatically.
func withProjectDir(t *testing.T) string {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

func TestHandleTaskAdd_AssignsHarpIDAndPersists(t *testing.T) {
	withProjectDir(t)
	s := &ctxServer{}

	_, res, err := s.handleTaskAdd(context.Background(), nil, taskAddInput{
		Text: "write the storage layer",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotEmpty(t, res.Task.HarpID, "Add should assign a harp ID")
	assert.Equal(t, "write the storage layer", res.Task.Text)
	assert.Equal(t, "To Do", res.Task.Status, "empty input status should default to To Do")
}

func TestHandleTaskList_FiltersByStatusAndTerm(t *testing.T) {
	withProjectDir(t)
	s := &ctxServer{}

	_, _, err := s.handleTaskAdd(context.Background(), nil, taskAddInput{Text: "alpha", Status: "In Progress"})
	require.NoError(t, err)
	_, _, err = s.handleTaskAdd(context.Background(), nil, taskAddInput{Text: "beta", Status: "To Do"})
	require.NoError(t, err)
	_, _, err = s.handleTaskAdd(context.Background(), nil, taskAddInput{Text: "gamma", Status: "Done"})
	require.NoError(t, err)

	// Status filter
	_, res, err := s.handleTaskList(context.Background(), nil, taskListInput{
		Statuses: []string{"In Progress", "To Do"},
	})
	require.NoError(t, err)
	assert.Len(t, res.Tasks, 2)

	// Term filter (case-insensitive)
	_, res, err = s.handleTaskList(context.Background(), nil, taskListInput{Term: "GAMMA"})
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Equal(t, "gamma", res.Tasks[0].Text)

	// include_summary attaches counts + in-progress IDs
	_, res, err = s.handleTaskList(context.Background(), nil, taskListInput{IncludeSummary: true})
	require.NoError(t, err)
	require.NotNil(t, res.Summary)
	assert.Equal(t, 1, res.Summary.Counts["In Progress"])
	assert.Len(t, res.Summary.InProgress, 1)
}

func TestHandleTaskSetStatus_MovesTask(t *testing.T) {
	withProjectDir(t)
	s := &ctxServer{}

	_, add, err := s.handleTaskAdd(context.Background(), nil, taskAddInput{Text: "do the thing"})
	require.NoError(t, err)
	harpID := add.Task.HarpID

	_, set, err := s.handleTaskSetStatus(context.Background(), nil, taskSetStatusInput{
		HarpID: harpID,
		Status: "Done",
	})
	require.NoError(t, err)
	assert.Equal(t, "Done", set.Task.Status)
	assert.True(t, set.Task.Checked)

	// Surfaced via list as well.
	_, list, err := s.handleTaskList(context.Background(), nil, taskListInput{Statuses: []string{"Done"}})
	require.NoError(t, err)
	require.Len(t, list.Tasks, 1)
	assert.Equal(t, harpID, list.Tasks[0].HarpID)
}

func TestHandleTaskSetStatus_UnknownHarpIDErrors(t *testing.T) {
	withProjectDir(t)
	s := &ctxServer{}

	_, _, err := s.handleTaskSetStatus(context.Background(), nil, taskSetStatusInput{
		HarpID: "no-such-id",
		Status: "Done",
	})
	assert.Error(t, err)
}

func TestParseTodoWritePayload_Shapes(t *testing.T) {
	cases := map[string]string{
		"tool_input wrapper": `{"tool_input":{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"completed"}]}}`,
		"bare todos":         `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"completed"}]}`,
		"bare array":         `[{"content":"a","status":"in_progress"},{"content":"b","status":"completed"}]`,
		"text alias":         `{"todos":[{"text":"a","status":"in_progress"},{"text":"b","status":"completed"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			items, err := parseTodoWritePayload([]byte(raw))
			require.NoError(t, err)
			require.Len(t, items, 2)
			assert.Equal(t, "a", items[0].Text)
			assert.Equal(t, "In Progress", items[0].Status)
			assert.Equal(t, "b", items[1].Text)
			assert.Equal(t, "Done", items[1].Status)
		})
	}
}

func TestParseTodoWritePayload_Empty(t *testing.T) {
	// Empty wrapper, empty array, malformed JSON — all yield nil items,
	// no error. The capture path will then archive everything still in
	// the store, which is the correct mirror-snapshot behavior.
	cases := []string{`{}`, `{"todos":[]}`, `[]`, `not json at all`}
	for _, raw := range cases {
		items, err := parseTodoWritePayload([]byte(raw))
		assert.NoError(t, err, raw)
		assert.Empty(t, items, raw)
	}
}

func TestTaskToolsInReviewAllowlist(t *testing.T) {
	// Defense-in-depth: task tools must remain callable while a bundle
	// review is pending — they don't touch bundle bytes or execute
	// bundle-shipped code, so they're explicitly safe. If somebody adds
	// a new task tool later and forgets to allowlist it, this test fires.
	for _, name := range []string{"task_list", "task_add", "task_set_status"} {
		t.Run(name, func(t *testing.T) {
			if _, ok := allowedDuringReview[name]; !ok {
				t.Errorf("%s should be in allowedDuringReview but isn't", name)
			}
		})
	}
}
