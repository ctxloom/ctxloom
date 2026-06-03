package tasks

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStatusTrigger(t *testing.T) {
	assert.ErrorIs(t, ValidateStatusTrigger(StatusDeferred, ""), ErrTriggerRequired)
	assert.ErrorIs(t, ValidateStatusTrigger(StatusDeferred, "   "), ErrTriggerRequired)
	assert.NoError(t, ValidateStatusTrigger(StatusDeferred, "v2 ships"))
	assert.NoError(t, ValidateStatusTrigger(StatusToDo, ""))
}

// markdownStore and logStore exercise both backends through the same Store API.
func markdownStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	return s
}

func logStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenLog(filepath.Join(t.TempDir(), "tasks.jsonl"), "sess")
	require.NoError(t, err)
	return s
}

func eachBackend(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Run("markdown", func(t *testing.T) { fn(t, markdownStore(t)) })
	t.Run("log", func(t *testing.T) { fn(t, logStore(t)) })
}

func TestDeferredRequiresTrigger(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *Store) {
		_, err := s.AddWithTrigger("park me", StatusDeferred, "")
		assert.ErrorIs(t, err, ErrTriggerRequired, "adding Deferred without a trigger must fail")

		task, err := s.AddWithTrigger("park me", StatusDeferred, "the API stabilizes")
		require.NoError(t, err)
		assert.Equal(t, StatusDeferred, task.Status)
		assert.Equal(t, "the API stabilizes", task.Trigger)
		assert.False(t, task.Checked, "Deferred is not a completed status")
	})
}

func TestSetStatusDeferredTriggerRules(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *Store) {
		task, err := s.Add("do later", StatusToDo)
		require.NoError(t, err)

		// To Do -> Deferred without a trigger is rejected.
		_, err = s.SetStatus(task.HarpID, StatusDeferred)
		assert.ErrorIs(t, err, ErrTriggerRequired)

		// With a trigger it succeeds and the trigger is stored.
		got, err := s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "the spike lands")
		require.NoError(t, err)
		assert.Equal(t, "the spike lands", got.Trigger)

		// Deferred -> To Do preserves the trigger (not silently dropped) and is
		// allowed without supplying one.
		got, err = s.SetStatus(task.HarpID, StatusToDo)
		require.NoError(t, err)
		assert.Equal(t, "the spike lands", got.Trigger, "leaving Deferred must keep the trigger")

		// Re-deferring with no new trigger reuses the preserved one.
		got, err = s.SetStatus(task.HarpID, StatusDeferred)
		require.NoError(t, err)
		assert.Equal(t, "the spike lands", got.Trigger, "re-deferring reuses the stored trigger")

		// A new trigger overrides the old one.
		got, err = s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "v2 is cut")
		require.NoError(t, err)
		assert.Equal(t, "v2 is cut", got.Trigger)
	})
}

func TestTriggerRoundTrips(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *Store) {
		// A trigger containing the comment terminator must survive the markdown
		// round-trip without corrupting the file.
		added, err := s.AddWithTrigger("tricky", StatusDeferred, "when x --> y happens")
		require.NoError(t, err)

		got, err := s.List([]string{StatusDeferred}, "")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, added.HarpID, got[0].HarpID)
		assert.Equal(t, "tricky", got[0].Text, "trigger marker must not leak into the task text")
		assert.Equal(t, "when x --> y happens", got[0].Trigger)
	})
}

func TestDeferredStatusIsNotChecked(t *testing.T) {
	assert.False(t, statusIsDone(StatusDeferred))
	assert.True(t, statusIsDone(StatusDone))
	assert.True(t, statusIsDone(StatusArchived))
}
