package spool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A routed out/ message is DELETED, and what survives it is its identity in
// out/routed/: the record the coordinator consults so that a copy whose delete
// was interrupted is finished rather than routed again. These tests read the
// disk back rather than trusting a return value.

// routedPath is the record entry for identity, straight off the layout.
func routedPath(t *testing.T, m PathMapper, identity string) string {
	t.Helper()
	root, err := Root(m, testHarp)
	require.NoError(t, err)
	return filepath.Join(root, "out", "routed", identity)
}

// routedOnly writes a routed record entry and nothing else, stamped at.
func routedOnly(t *testing.T, m PathMapper, identity string, at time.Time) {
	t.Helper()
	path := routedPath(t, m, identity)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chtimes(path, at, at))
}

func TestConsume_RecordsTheIdentityThenDeletesTheFile(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedOut(t, m, "routed once\n")
	id := entryFor(t, m, ref).Identity()

	require.NoError(t, Consume(m, ref, id, time.Now()))

	assert.Empty(t, filesIn(t, m, DirOut), "a routed message is deleted from out/")
	assert.FileExists(t, routedPath(t, m, id), "its identity is what survives it")
	got, err := Routed(m, testHarp, id)
	require.NoError(t, err)
	assert.True(t, got)
	other, err := Routed(m, testHarp, "m-never")
	require.NoError(t, err)
	assert.False(t, other, "an identity nobody routed is not in the record")
	delivered, err := Delivered(m, testHarp, id)
	require.NoError(t, err)
	assert.False(t, delivered, "the routed record and the delivered record are separate")
}

// The record is written BEFORE the delete, so a record that cannot be written
// leaves the file where it was: it is routed again, never lost.
func TestConsume_ARecordThatCannotBeWrittenLeavesTheFile(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedOut(t, m, "keep me\n")

	err := Consume(m, ref, "../escape", time.Now())
	require.Error(t, err)
	assert.Equal(t, []string{ref.Name}, filesIn(t, m, DirOut), "an identity that cannot be recorded deletes nothing")

	block := filepath.Dir(routedPath(t, m, "x"))
	require.NoError(t, os.WriteFile(block, []byte("in the way"), 0o600))
	err = Consume(m, ref, entryFor(t, m, ref).Identity(), time.Now())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrAlreadyGone)
	assert.Contains(t, filesIn(t, m, DirOut), ref.Name, "nothing recorded, so nothing deleted")
}

// CRASH BETWEEN RECORD AND DELETE: the file is still in out/ and its identity
// is recorded. Routed says so, and Consume finishes the delete.
func TestConsume_ARecordedFileIsFinishedByTheNextConsume(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedOut(t, m, "interrupted\n")
	id := entryFor(t, m, ref).Identity()
	routedOnly(t, m, id, time.Now())

	recorded, err := Routed(m, testHarp, id)
	require.NoError(t, err)
	require.True(t, recorded, "the record outlives the interrupted delete")

	require.NoError(t, Consume(m, ref, id, time.Now()))
	assert.Empty(t, filesIn(t, m, DirOut), "the interrupted delete is finished")
	assert.FileExists(t, routedPath(t, m, id))
}

func TestConsume_AnInboxMessageIsNotConsumable(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "inbox\n")
	err := Consume(m, ref, "m-in", time.Now())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAlreadyGone)
	assert.Equal(t, []string{ref.Name}, filesIn(t, m, DirIn))
	routed, err := Routed(m, testHarp, "m-in")
	require.NoError(t, err)
	assert.False(t, routed, "a refused consume records nothing")
}

// TestConsume_SecondTakeIsAlreadyGone: the loser of a consume race gets the
// typed sentinel, and the identity is recorded either way.
func TestConsume_SecondTakeIsAlreadyGone(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedOut(t, m, "once\n")
	id := entryFor(t, m, ref).Identity()
	require.NoError(t, Consume(m, ref, id, time.Now()))

	err := Consume(m, ref, id, time.Now())
	require.ErrorIs(t, err, ErrAlreadyGone, "a lost consume race must be ErrAlreadyGone, not a generic failure")
	assert.FileExists(t, routedPath(t, m, id))
}

// The routed record is pruned on every Consume past the same window the
// delivered record keeps.
func TestConsume_PrunesRoutedEntriesOlderThanTheRetentionWindow(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	now := time.Now()
	routedOnly(t, m, "m-old", now.Add(-DeliveredRetention-time.Minute))
	routedOnly(t, m, "m-young", now.Add(-DeliveredRetention+time.Minute))
	recordOnly(t, m, "m-old-delivered", now.Add(-DeliveredRetention-time.Minute))

	ref, _ := seedOut(t, m, "x\n")
	id := entryFor(t, m, ref).Identity()
	require.NoError(t, Consume(m, ref, id, now))

	for name, want := range map[string]bool{"m-old": false, "m-young": true, id: true} {
		got, err := Routed(m, testHarp, name)
		require.NoError(t, err)
		assert.Equal(t, want, got, "routed record entry %s", name)
	}
	kept, err := Delivered(m, testHarp, "m-old-delivered")
	require.NoError(t, err)
	assert.True(t, kept, "a routed prune never touches the delivered record")
}

func TestRouted_RefusesAnIdentityThatIsNotAFileName(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	_, err := Routed(m, testHarp, "../escape")
	assert.Error(t, err)
}
