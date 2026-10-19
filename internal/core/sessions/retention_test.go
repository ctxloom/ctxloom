package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A session's transcript is its record. It becomes reclaimable once the
// session ENDED at or before the retention cutoff, unless it is marked keep.
func TestRetention_ReclaimableOnlyOnceEndedPastTheCutoffAndNotKept(t *testing.T) {
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before, after := cutoff.Add(-time.Hour), cutoff.Add(time.Hour)
	r := Retention{Cutoff: cutoff}

	assert.NoError(t, r.Retains(&Entry{EndedAt: &before}, before), "ended before the cutoff: reclaimable")
	assert.NoError(t, r.Retains(&Entry{EndedAt: &cutoff}, cutoff), "ended exactly at the cutoff: reclaimable")

	err := r.Retains(&Entry{EndedAt: &after}, after)
	require.ErrorIs(t, err, ErrTranscriptRecent)
	assert.NotErrorIs(t, err, ErrTranscriptKept)

	err = r.Retains(&Entry{EndedAt: &before, Keep: true}, before)
	require.ErrorIs(t, err, ErrTranscriptKept, "keep wins over age")
	assert.NotErrorIs(t, err, ErrTranscriptRecent)
}

// When the session ended is the LATER of its recorded end and its last
// activity: a session resumed after its recorded end, or one that died with
// no end recorded at all, has not ended before it was last worked on.
func TestRetention_EndIsTheLaterOfRecordedEndAndLastActivity(t *testing.T) {
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	old, recent := cutoff.Add(-48*time.Hour), cutoff.Add(time.Hour)
	r := Retention{Cutoff: cutoff}

	require.ErrorIs(t, r.Retains(&Entry{EndedAt: &old}, recent), ErrTranscriptRecent,
		"worked on after its recorded end: it ended no earlier than that")
	assert.NoError(t, r.Retains(&Entry{}, old), "no recorded end: its last activity is its end")
	require.ErrorIs(t, r.Retains(&Entry{}, time.Time{}), ErrTranscriptRecent,
		"no recorded end and no activity: nothing proves it ended, so it is kept")
}

// An internal one-shot's transcript is a machine's single turn, never a
// human's record: it is reclaimable at any age — unless marked keep.
func TestRetention_OneShotIsReclaimableAtAnyAgeUnlessKept(t *testing.T) {
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	recent := cutoff.Add(time.Hour)
	r := Retention{Cutoff: cutoff}

	assert.NoError(t, r.Retains(&Entry{Origin: OriginOneShot, EndedAt: &recent}, recent))
	require.ErrorIs(t, r.Retains(&Entry{Origin: OriginOneShot, EndedAt: &recent, Keep: true}, recent), ErrTranscriptKept)
	require.ErrorIs(t, r.Retains(&Entry{Origin: OriginAgent, EndedAt: &recent}, recent), ErrTranscriptRecent,
		"a delegated child's session is a record like a human's")
}

// No cutoff is no rule: refused, never read as "everything is old enough".
func TestRetention_ZeroCutoffRefuses(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	require.ErrorIs(t, Retention{}.Retains(&Entry{EndedAt: &old}, old), ErrNoRetentionBound)
}

// `session keep` writes the mark to the sidecar and --undo clears it; the
// mark survives a re-read, and a harp with no sidecar is ErrNotFound.
func TestManager_SetKeepPersistsAndClears(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "swift-amber-falcon", "project_dir: /proj\nstarted_at: 2026-09-01T10:00:00Z\n")

	require.NoError(t, m.SetKeep("swift-amber-falcon", true))
	e, err := m.Find("swift-amber-falcon")
	require.NoError(t, err)
	assert.True(t, e.Keep)
	assert.Contains(t, string(readSidecarFile(t, root, "swift-amber-falcon")), "keep: true")

	require.NoError(t, m.SetKeep("swift-amber-falcon", false))
	e, err = m.Find("swift-amber-falcon")
	require.NoError(t, err)
	assert.False(t, e.Keep)
	assert.NotContains(t, string(readSidecarFile(t, root, "swift-amber-falcon")), "keep:")

	require.ErrorIs(t, m.SetKeep("no-such-harp", true), ErrNotFound)
}

// A session's transcript activity is when its CONVERSATION was last written
// — the transcripts and the engine's native history — never its record: a
// sidecar write (`session keep`, its --undo, a purge mark) is not the session
// being worked on, and must not restart its retention.
func TestTranscriptActivity_IsTheConversationNotTheRecord(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron") // every mtime backdated to reapOldEnough
	reapWriteSidecar(t, dir, "keep: true\n")  // the record, written now

	got, err := TranscriptActivity(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.WithinDuration(t, reapOldEnough, got, time.Second, "the record's write is not transcript activity")

	ok, err := ActivityTime(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, ok.After(got), "the session dir as a whole did move: ActivityTime sees the record")
}

// A session with no transcript and no native history has no transcript
// activity: zero, not an error.
func TestTranscriptActivity_NoConversationIsZero(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, paths.TranscriptsDirName)))
	require.NoError(t, os.RemoveAll(filepath.Join(dir, paths.NativeDirName)))

	got, err := TranscriptActivity(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, got.IsZero())
}
