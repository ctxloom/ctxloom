package tasks

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStatusTrigger(t *testing.T) {
	assert.ErrorIs(t, ValidateStatusTrigger(StatusDeferred, ""), ErrTriggerRequired)
	assert.ErrorIs(t, ValidateStatusTrigger(StatusDeferred, "   "), ErrTriggerRequired)
	assert.NoError(t, ValidateStatusTrigger(StatusDeferred, "v2 ships"))
	assert.NoError(t, ValidateStatusTrigger(StatusToDo, ""))
}

func logStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenLog(filepath.Join(t.TempDir(), "taskloom.jsonl"), "sess")
	require.NoError(t, err)
	return s
}

func TestDeferredRequiresTrigger(t *testing.T) {
	s := logStore(t)
	_, err := s.AddWithTrigger("park me", StatusDeferred, "")
	assert.ErrorIs(t, err, ErrTriggerRequired, "adding Deferred without a trigger must fail")

	task, err := s.AddWithTrigger("park me", StatusDeferred, "the API stabilizes")
	require.NoError(t, err)
	assert.Equal(t, StatusDeferred, task.Status)
	assert.Equal(t, "the API stabilizes", task.Trigger)
	assert.False(t, task.Checked, "Deferred is not a completed status")
}

func TestSetStatusDeferredTriggerRules(t *testing.T) {
	s := logStore(t)
	task, err := s.AddWithTrigger("do later", StatusToDo, "")
	require.NoError(t, err)

	// To Do -> Deferred without a trigger is rejected.
	_, err = s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "")
	assert.ErrorIs(t, err, ErrTriggerRequired)

	// With a trigger it succeeds and the trigger is stored.
	got, err := s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "the spike lands")
	require.NoError(t, err)
	assert.Equal(t, "the spike lands", got.Trigger)

	// Deferred -> To Do preserves the trigger (not silently dropped) and is
	// allowed without supplying one.
	got, err = s.SetStatusWithTrigger(task.HarpID, StatusToDo, "")
	require.NoError(t, err)
	assert.Equal(t, "the spike lands", got.Trigger, "leaving Deferred must keep the trigger")

	// Re-deferring with no new trigger reuses the preserved one.
	got, err = s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "")
	require.NoError(t, err)
	assert.Equal(t, "the spike lands", got.Trigger, "re-deferring reuses the stored trigger")

	// A new trigger overrides the old one.
	got, err = s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "v2 is cut")
	require.NoError(t, err)
	assert.Equal(t, "v2 is cut", got.Trigger)
}

func TestTriggerRoundTrips(t *testing.T) {
	s := logStore(t)
	added, err := s.AddWithTrigger("tricky", StatusDeferred, "when x --> y happens")
	require.NoError(t, err)

	got, err := s.List([]string{StatusDeferred}, "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, added.HarpID, got[0].HarpID)
	assert.Equal(t, "tricky", got[0].Text)
	assert.Equal(t, "when x --> y happens", got[0].Trigger)
}

func TestDeferredStatusIsNotChecked(t *testing.T) {
	assert.False(t, statusIsDone(StatusDeferred))
	assert.True(t, statusIsDone(StatusDone))
	assert.True(t, statusIsDone(StatusArchived))
}

func TestSetTextReplacesInPlace(t *testing.T) {
	s := logStore(t)
	orig, err := s.AddWithTrigger("old text", StatusDeferred, "when ready")
	require.NoError(t, err)

	got, err := s.SetText(orig.HarpID, "  brand new text  ")
	require.NoError(t, err)
	assert.Equal(t, orig.HarpID, got.HarpID, "harp identity is stable across an edit")
	assert.Equal(t, "brand new text", got.Text, "text is replaced and trimmed")
	assert.NotEqual(t, orig.TextHash, got.TextHash, "text hash tracks the new text")
	assert.Equal(t, StatusDeferred, got.Status, "edit leaves status untouched")
	assert.Equal(t, "when ready", got.Trigger, "edit leaves the trigger untouched")

	// The change survives a fresh read of the store.
	list, err := s.List([]string{StatusDeferred}, "")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "brand new text", list[0].Text)
}

func TestSetTextErrors(t *testing.T) {
	s := logStore(t)
	task, err := s.AddWithTrigger("real task", StatusToDo, "")
	require.NoError(t, err)

	_, err = s.SetText(task.HarpID, "   ")
	assert.Error(t, err, "empty replacement text is rejected")

	_, err = s.SetText("no-such-harp", "whatever")
	assert.Error(t, err, "editing an unknown harp errors")
}

// TestDeferredSince pins the accessor trigger evaluation relies on to scope
// git evidence to "what happened after this task was parked": only currently
// Deferred tasks appear, keyed by the timestamp of the event that most
// recently moved them into Deferred (an add, or a later status change).
func TestDeferredSince(t *testing.T) {
	s := logStore(t)

	// Deferred directly via add.
	viaAdd, err := s.AddWithTrigger("park at birth", StatusDeferred, "when x happens")
	require.NoError(t, err)

	// Starts elsewhere, deferred later — DeferredSince must use the LATER
	// event's timestamp, not the add's.
	viaStatus, err := s.AddWithTrigger("do later", StatusToDo, "")
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond) // guarantee a distinct, later timestamp
	_, err = s.SetStatusWithTrigger(viaStatus.HarpID, StatusDeferred, "when y happens")
	require.NoError(t, err)

	// Never deferred at all — must not appear.
	neverDeferred, err := s.AddWithTrigger("just doing it", StatusToDo, "")
	require.NoError(t, err)

	// Deferred, then revived — no longer Deferred, so must not appear even
	// though it carries a preserved trigger.
	revived, err := s.AddWithTrigger("came back", StatusDeferred, "when z happens")
	require.NoError(t, err)
	_, err = s.SetStatusWithTrigger(revived.HarpID, StatusToDo, "")
	require.NoError(t, err)

	since, err := s.DeferredSince()
	require.NoError(t, err)

	require.Contains(t, since, viaAdd.HarpID)
	require.Contains(t, since, viaStatus.HarpID)
	assert.NotContains(t, since, neverDeferred.HarpID)
	assert.NotContains(t, since, revived.HarpID)
	assert.True(t, since[viaStatus.HarpID].After(since[viaAdd.HarpID]),
		"the status-change deferral timestamp must be later than the add-time deferral")
}

// TestDeferredSinceReDeferralUsesLatestTransition covers cycling
// Deferred -> To Do -> Deferred: the timestamp must track the MOST RECENT
// entry into Deferred, not the first.
func TestDeferredSinceReDeferralUsesLatestTransition(t *testing.T) {
	s := logStore(t)
	task, err := s.AddWithTrigger("cycle me", StatusDeferred, "first condition")
	require.NoError(t, err)

	_, err = s.SetStatusWithTrigger(task.HarpID, StatusToDo, "")
	require.NoError(t, err)

	firstSince, err := s.DeferredSince()
	require.NoError(t, err)
	assert.NotContains(t, firstSince, task.HarpID, "not currently Deferred")

	time.Sleep(2 * time.Millisecond)
	_, err = s.SetStatusWithTrigger(task.HarpID, StatusDeferred, "")
	require.NoError(t, err)

	secondSince, err := s.DeferredSince()
	require.NoError(t, err)
	require.Contains(t, secondSince, task.HarpID)
}

func TestDeferredSinceEmptyStoreReturnsEmptyMap(t *testing.T) {
	s := logStore(t)
	since, err := s.DeferredSince()
	require.NoError(t, err)
	assert.Empty(t, since)
}

func TestListFiltersByStatusAndTerm(t *testing.T) {
	s := logStore(t)
	_, err := s.AddWithTrigger("alpha task", StatusInProgress, "")
	require.NoError(t, err)
	tB, err := s.AddWithTrigger("beta task", StatusToDo, "")
	require.NoError(t, err)
	tC, err := s.AddWithTrigger("gamma task", StatusDone, "")
	require.NoError(t, err)

	got, err := s.List([]string{StatusInProgress, StatusToDo}, "")
	require.NoError(t, err)
	assert.Len(t, got, 2, "status filter")

	got, err = s.List(nil, "BETA")
	require.NoError(t, err)
	require.Len(t, got, 1, "term filter is case-insensitive")
	assert.Equal(t, tB.HarpID, got[0].HarpID)

	got, err = s.List([]string{StatusDone}, "gamma")
	require.NoError(t, err)
	require.Len(t, got, 1, "combined filter")
	assert.Equal(t, tC.HarpID, got[0].HarpID)

	got, err = s.List([]string{StatusDone}, "alpha")
	require.NoError(t, err)
	assert.Empty(t, got, "no-match filter")
}
