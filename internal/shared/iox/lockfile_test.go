package iox

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenLockFile_CreatesAbsentFileEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")

	f, err := OpenLockFile(path, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Zero(t, info.Size())
}

func TestOpenLockFile_LeavesExistingContentAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	require.NoError(t, WriteFileAtomic(path, []byte("held"), 0o600))

	f, err := OpenLockFile(path, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "held", string(got), "opening a lock file must never truncate it")
}

func TestOpenLockFile_HandleCannotWrite(t *testing.T) {
	f, err := OpenLockFile(filepath.Join(t.TempDir(), "x.lock"), 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	_, err = f.Write([]byte("x"))
	assert.Error(t, err, "the handle exists to be locked, not written through")
}

// A missing parent surfaces as fs.ErrNotExist: callers opening inside a dir
// that may have been deleted under them branch on exactly that.
func TestOpenLockFile_MissingParentIsErrNotExist(t *testing.T) {
	_, err := OpenLockFile(filepath.Join(t.TempDir(), "gone", "x.lock"), 0o600)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}
