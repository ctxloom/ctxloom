package spool

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The reader-neutral peeks (HasMail, PendingNames, Unread) answer "nothing"
// only for a spool that holds nothing. A spool they cannot read is an error,
// never an empty inbox: an empty answer would let mail wait unannounced.

// unreadableIn puts a plain file where harp's in/ directory belongs.
func unreadableIn(t *testing.T, m PathMapper) {
	t.Helper()
	in, err := DirPath(m, testHarp, DirIn)
	require.NoError(t, err)
	testsupport.WriteFile(t, afero.NewOsFs(), in, []byte("not a directory"), 0o600)
}

func TestPeeks_AnUnreadableInboxIsAnError(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	unreadableIn(t, m)
	fs := afero.NewOsFs()

	_, err := HasMail(fs, m, testHarp)
	assert.Error(t, err, "HasMail")
	_, err = PendingNames(fs, m, testHarp)
	assert.Error(t, err, "PendingNames")
	_, err = Unread(fs, m, testHarp)
	assert.Error(t, err, "Unread")
}

func TestPeeks_ABadHarpIsRefused(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	fs := afero.NewOsFs()

	_, err := HasMail(fs, m, "../escape")
	assert.Error(t, err, "HasMail")
	_, err = PendingNames(fs, m, "../escape")
	assert.Error(t, err, "PendingNames")
}

// TestUnread_AnUnreadableDeliveredRecordIsAnError: with mail waiting, the
// delivered record decides what is unread; one that cannot be listed is an
// error, not "everything is unread".
func TestUnread_AnUnreadableDeliveredRecordIsAnError(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedIn(t, m, "waiting\n")
	dir, err := deliveredDir(m, testHarp)
	require.NoError(t, err)
	testsupport.WriteFile(t, afero.NewOsFs(), dir, []byte("not a directory"), 0o600)

	_, err = Unread(afero.NewOsFs(), m, testHarp)
	assert.Error(t, err)
}
