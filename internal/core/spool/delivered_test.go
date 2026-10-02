package spool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A delivered in/ message is DELETED, and what survives it is its identity in
// in/delivered/: the record every reader consults so that a copy of something
// already delivered is not delivered again. These tests read the disk back
// rather than trusting a return value.

// deliveredPath is the record entry for identity, straight off the layout.
func deliveredPath(t *testing.T, m PathMapper, identity string) string {
	t.Helper()
	root, err := Root(m, testHarp)
	require.NoError(t, err)
	return filepath.Join(root, "in", "delivered", identity)
}

// recordOnly writes a record entry and nothing else: the state a crash
// between Deliver's record and its delete leaves behind.
func recordOnly(t *testing.T, m PathMapper, identity string, at time.Time) {
	t.Helper()
	path := deliveredPath(t, m, identity)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, os.Chtimes(path, at, at))
}

func entryFor(t *testing.T, m PathMapper, ref Ref) Entry {
	t.Helper()
	res, err := Sweep(m, testHarp, ref.Dir)
	require.NoError(t, err)
	for _, e := range res.Entries {
		if e.Ref.Name == ref.Name {
			return e
		}
	}
	t.Fatalf("%s is not in %s", ref.Name, ref.Dir)
	return Entry{}
}

func TestDeliver_RecordsTheIdentityThenDeletesTheFile(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "hello\n")
	id := entryFor(t, m, ref).Identity()

	require.NoError(t, Deliver(m, ref, id, time.Now()))

	assert.Empty(t, filesIn(t, m, DirIn), "a delivered message is deleted from in/")
	assert.FileExists(t, deliveredPath(t, m, id), "its identity is what survives it")
	got, err := Delivered(m, testHarp, id)
	require.NoError(t, err)
	assert.True(t, got)
	other, err := Delivered(m, testHarp, "m-never")
	require.NoError(t, err)
	assert.False(t, other, "an identity nobody delivered is not in the record")
}

func TestDeliver_AClaimedEntryIsDeletedFromClaimed(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "m-claimed", "x\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)

	require.NoError(t, Deliver(m, res.Entries[0].Ref, res.Entries[0].Identity(), time.Now()))
	assert.Empty(t, filesIn(t, m, ClaimedDirName))
	assert.FileExists(t, deliveredPath(t, m, "m-claimed"))
}

// The record is written BEFORE the delete, so a record that cannot be written
// leaves the file where it was: at-least-once, never loss.
func TestDeliver_ARecordThatCannotBeWrittenLeavesTheFile(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "keep me\n")

	err := Deliver(m, ref, "../escape", time.Now())
	require.Error(t, err)
	assert.Equal(t, []string{ref.Name}, filesIn(t, m, DirIn), "an identity that cannot be recorded deletes nothing")

	// The record's directory cannot be created: a file sits where it goes.
	// The write fails AFTER every check has passed, so only the ORDER keeps
	// the message: record first, delete second.
	block := filepath.Dir(deliveredPath(t, m, "x"))
	require.NoError(t, os.WriteFile(block, []byte("in the way"), 0o600))
	err = Deliver(m, ref, entryFor(t, m, ref).Identity(), time.Now())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrAlreadyGone)
	assert.Contains(t, filesIn(t, m, DirIn), ref.Name, "nothing recorded, so nothing deleted")
}

func TestDeliver_AMissingFileIsAlreadyGoneButStillRecorded(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "x\n")
	id := entryFor(t, m, ref).Identity()
	require.NoError(t, Deliver(m, ref, id, time.Now()))

	err := Deliver(m, ref, id, time.Now())
	assert.ErrorIs(t, err, ErrAlreadyGone)
	assert.FileExists(t, deliveredPath(t, m, id))
}

func TestDeliver_RefusesADirectoryThatIsNotAnInbox(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	for _, d := range []Dir{DirOut, DirOutConsumed, DirInWithdrawn, FailedDirName} {
		err := Deliver(m, Ref{Harp: testHarp, Dir: d, Name: "1.1.coord.md"}, "m-1", time.Now())
		assert.Error(t, err, "%s is not delivered from", d)
	}
	recorded, err := Delivered(m, testHarp, "m-1")
	require.NoError(t, err)
	assert.False(t, recorded, "a refused delivery records nothing")
}

// An identity arriving again within the window after its delivery is
// rejected: the copy is deleted, never handed out.
func TestClaim_AnIdentityDeliveredWithinTheWindowIsRejected(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "mail-1", "first copy\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	require.NoError(t, Deliver(m, res.Entries[0].Ref, res.Entries[0].Identity(), time.Now()))

	seedOrigin(t, m, "mail-1", "second copy\n")
	res, err = Claim(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, res.Entries, "a message whose identity was delivered is not delivered again")
	assert.Empty(t, filesIn(t, m, DirIn))
	assert.Empty(t, filesIn(t, m, ClaimedDirName))
}

// CRASH BETWEEN RECORD AND DELETE, owner side: the file is still in
// in/claimed/ and its identity is recorded. The next Claim finishes the
// delete and does not hand it out again.
func TestClaim_ARecordedClaimedEntryIsFinishedNotRedelivered(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "m-crash", "x\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	recordOnly(t, m, "m-crash", time.Now())

	res, err = Claim(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, res.Entries, "a delivered message is not delivered twice")
	assert.Empty(t, filesIn(t, m, ClaimedDirName), "the interrupted delete is finished")
}

// CRASH BETWEEN RECORD AND DELETE, a file still in in/ (the runner's shape).
func TestClaim_ARecordedInboxEntryIsFinishedNotRedelivered(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "m-crash-in", "x\n")
	recordOnly(t, m, "m-crash-in", time.Now())

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, res.Entries)
	assert.Empty(t, filesIn(t, m, DirIn))
	assert.Empty(t, filesIn(t, m, ClaimedDirName))
}

// CRASH BEFORE THE RECORD: claimed, never delivered. The next Claim hands it
// out again — at-least-once, the floor — and it is not lost.
func TestClaim_AnUnrecordedClaimIsHandedOutAgain(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "m-unacked", "again\n")
	_, err := Claim(m, testHarp)
	require.NoError(t, err)

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{"again\n"}, bodiesOf(res.Entries))
}

// Compaction drops entries older than the window and keeps the newer ones;
// it runs on every Deliver.
func TestDeliver_PrunesRecordEntriesOlderThanTheRetentionWindow(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	now := time.Now()
	recordOnly(t, m, "m-old", now.Add(-DeliveredRetention-time.Minute))
	recordOnly(t, m, "m-young", now.Add(-DeliveredRetention+time.Minute))

	ref, _ := seedIn(t, m, "x\n")
	id := entryFor(t, m, ref).Identity()
	require.NoError(t, Deliver(m, ref, id, now))

	got, err := DeliveredIdentities(m, testHarp)
	require.NoError(t, err)
	assert.NotContains(t, got, "m-old", "older than the window: dropped")
	assert.Contains(t, got, "m-young", "inside the window: kept")
	assert.Contains(t, got, id)
}

func TestDeliveredIdentities_ListsNamesWithTheirTimesAndSkipsStaging(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	recordOnly(t, m, "m-a", at)
	recordOnly(t, m, ".m-b.123.tmp", at) // a WriteFileAtomic staging file

	got, err := DeliveredIdentities(m, testHarp)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got["m-a"].Equal(at))

	none, err := DeliveredIdentities(m, "never-made-one")
	require.NoError(t, err)
	assert.Empty(t, none, "a spool with no record has delivered nothing")
}

func TestDelivered_RefusesAnIdentityThatIsNotAFileName(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	_, err := Delivered(m, testHarp, "../escape")
	assert.Error(t, err)
}
