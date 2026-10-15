package spool

import (
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Unread is what a parent's roster reads off a child's inbox to say "this
// child has mail it has not taken yet". It is the inbox's own three states
// (unclaimed in/, claimed-but-unacknowledged in/claimed/, delivered) reduced
// to one number: everything not yet delivered, counted once per identity.

func unreadOf(t *testing.T, m PathMapper) int {
	t.Helper()
	n, err := Unread(afero.NewOsFs(), m, testHarp)
	require.NoError(t, err)
	return n
}

func TestUnread_ASpoolThatWasNeverCreatedHasNothingUnread(t *testing.T) {
	hostHome(t)
	assert.Zero(t, unreadOf(t, NewHomeMapper()))
}

func TestUnread_CountsEveryUndeliveredMessageOnce(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedIn(t, m, "one\n")
	seedIn(t, m, "two\n")
	assert.Equal(t, 2, unreadOf(t, m), "two messages wait in in/")

	res, err := Claim(afero.NewOsFs(), m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 2)
	assert.Equal(t, 2, unreadOf(t, m), "a CLAIMED message has not been delivered: it is still unread")

	require.NoError(t, Deliver(afero.NewOsFs(), m, res.Entries[0].Ref, res.Entries[0].Identity(), time.Now()))
	assert.Equal(t, 1, unreadOf(t, m), "a delivered message is no longer unread")

	seedOrigin(t, m, "dup", "a\n")
	seedOrigin(t, m, "dup", "b\n")
	assert.Equal(t, 2, unreadOf(t, m), "two copies of one identity are one unread message")
}

// A copy whose delete was interrupted after its record was written is
// delivered, even though its file is still in in/.
func TestUnread_ACopyAlreadyRecordedAsDeliveredIsNotUnread(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "done", "x\n")
	entry, err := deliveredEntry(m, testHarp, "done")
	require.NoError(t, err)
	require.NoError(t, recordDelivered(afero.NewOsFs(), entry, "done"))
	assert.Zero(t, unreadOf(t, m))
}

// With nothing waiting there is nothing to subtract: Unread does not consult
// the delivered record at all, so a record it could not list (here a file
// where its directory belongs) does not turn "no mail" into an error.
func TestUnread_NothingWaitingNeverReadsTheDeliveredRecord(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	dir, err := deliveredDir(m, testHarp)
	require.NoError(t, err)
	fs := afero.NewOsFs()
	testsupport.WriteFile(t, fs, dir, []byte("not a directory"), 0o600)
	_, err = DeliveredIdentities(fs, m, testHarp)
	require.Error(t, err, "premise: the delivered record cannot be listed")

	n, err := Unread(fs, m, testHarp)
	require.NoError(t, err)
	assert.Zero(t, n)
}

// HasMail is the mail-peek hook's cheap check, made before it asks anyone
// anything: does a file wait in in/, or (the owner's reader having claimed it
// and not acknowledged it) in in/claimed/? It reads directory listings only.
func TestHasMail_SeesInAndClaimedAndNothingElse(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	has := func() bool {
		ok, err := HasMail(afero.NewOsFs(), m, testHarp)
		require.NoError(t, err)
		return ok
	}
	assert.False(t, has(), "a spool that was never created has no mail")
	require.NoError(t, EnsureDirs(afero.NewOsFs(), m, testHarp))
	assert.False(t, has(), "empty directories are no mail")

	seedIn(t, m, "waiting\n")
	assert.True(t, has(), "a file in in/")

	res, err := Claim(afero.NewOsFs(), m, testHarp)
	require.NoError(t, err)
	assert.True(t, has(), "a claimed, unacknowledged file is still mail")

	require.NoError(t, Deliver(afero.NewOsFs(), m, res.Entries[0].Ref, res.Entries[0].Identity(), time.Now()))
	assert.False(t, has(), "a delivered message is gone; its record is not mail")
}
