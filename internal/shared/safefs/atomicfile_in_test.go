package safefs

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A NewAtomicFileIn file exists for a writer that learns its destination
// name only after writing — a content-addressed store naming a blob by the
// hash of the bytes it just streamed. These pin CommitAs as Commit with the
// name supplied late: same guard, same durability, same one-shot rule, and a
// destination confined to the directory the temp was created in (a rename
// across directories is neither atomic everywhere nor covered by one
// directory sync).

func newMemDir(t *testing.T, dir string) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	return fs
}

func dirNames(t *testing.T, fs afero.Fs, dir string) []string {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAtomicFileIn_CommitAsInstallsUnderTheNameChosenAfterWriting(t *testing.T) {
	fs := newMemDir(t, "/store")
	af, err := NewAtomicFileIn(fs, "/store", 0o600)
	require.NoError(t, err)

	names := dirNames(t, fs, "/store")
	require.Len(t, names, 1, "exactly the temp file exists before CommitAs")
	assert.True(t, strings.HasPrefix(names[0], ".atomic-") && strings.HasSuffix(names[0], ".tmp"),
		"temp %q must be a hidden .atomic-*.tmp", names[0])

	_, err = af.Write([]byte("named later"))
	require.NoError(t, err)
	require.NoError(t, af.CommitAs("/store/abc"))

	got, err := afero.ReadFile(fs, "/store/abc")
	require.NoError(t, err)
	assert.Equal(t, "named later", string(got))
	assert.Equal(t, []string{"abc"}, dirNames(t, fs, "/store"), "no temp file survives CommitAs")
	info, err := fs.Stat("/store/abc")
	require.NoError(t, err)
	assert.Equal(t, "-rw-------", info.Mode().Perm().String(), "perm is applied exactly, as Commit does")
}

func TestAtomicFileIn_CommitAsOutsideTheTempDirIsRefused(t *testing.T) {
	fs := newMemDir(t, "/store")
	require.NoError(t, fs.MkdirAll("/elsewhere", 0o755))
	af, err := NewAtomicFileIn(fs, "/store", 0o600)
	require.NoError(t, err)
	_, err = af.Write([]byte("x"))
	require.NoError(t, err)

	err = af.CommitAs("/elsewhere/abc")
	require.ErrorIs(t, err, ErrAtomicFileCrossDir)

	exists, err := afero.Exists(fs, "/elsewhere/abc")
	require.NoError(t, err)
	assert.False(t, exists, "a refused CommitAs installs nothing")
	assert.Empty(t, dirNames(t, fs, "/store"), "a refused CommitAs removes the temp")
	assert.ErrorIs(t, af.CommitAs("/store/abc"), ErrAtomicFileDone, "a refused CommitAs consumes the file")
}

func TestAtomicFileIn_IsOneShot(t *testing.T) {
	fs := newMemDir(t, "/store")
	af, err := NewAtomicFileIn(fs, "/store", 0o600)
	require.NoError(t, err)
	_, err = af.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, af.CommitAs("/store/a"))

	assert.ErrorIs(t, af.CommitAs("/store/b"), ErrAtomicFileDone)
	assert.ErrorIs(t, af.Commit(), ErrAtomicFileDone)
	assert.ErrorIs(t, af.Abort(), ErrAtomicFileDone)
	_, werr := af.Write([]byte("y"))
	assert.ErrorIs(t, werr, ErrAtomicFileDone)
}

func TestAtomicFileIn_CommitWithoutADestinationIsRefused(t *testing.T) {
	fs := newMemDir(t, "/store")
	af, err := NewAtomicFileIn(fs, "/store", 0o600)
	require.NoError(t, err)
	_, err = af.Write([]byte("x"))
	require.NoError(t, err)

	require.ErrorIs(t, af.Commit(), ErrAtomicFileNoPath)
	assert.Empty(t, dirNames(t, fs, "/store"), "a refused Commit removes the temp")
	assert.ErrorIs(t, af.CommitAs("/store/a"), ErrAtomicFileDone)
}

// Commit on a NewAtomicFile file is CommitAs(its own path): the existing
// contract must not have moved.
func TestAtomicFile_CommitIsCommitAsItsOwnPath(t *testing.T) {
	fs := newMemDir(t, "/p")
	af, err := NewAtomicFile(fs, "/p/out", 0o600)
	require.NoError(t, err)
	_, err = af.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, af.Commit())
	got, err := afero.ReadFile(fs, "/p/out")
	require.NoError(t, err)
	assert.Equal(t, "x", string(got))
}

func TestAtomicFileIn_EmptyCommitAsOverExisting_GuardedUnlessAllowEmpty(t *testing.T) {
	fs := newMemDir(t, "/store")
	require.NoError(t, afero.WriteFile(fs, "/store/live", []byte("keep"), 0o600))

	af, err := NewAtomicFileIn(fs, "/store", 0o600)
	require.NoError(t, err)
	require.ErrorIs(t, af.CommitAs("/store/live"), ErrEmptyOverwrite)
	got, err := afero.ReadFile(fs, "/store/live")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(got), "the guard leaves the existing file intact")

	af, err = NewAtomicFileIn(fs, "/store", 0o600, AllowEmpty())
	require.NoError(t, err)
	require.NoError(t, af.CommitAs("/store/live"))
	got, err = afero.ReadFile(fs, "/store/live")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAtomicFileIn_CommitAs_DurableSyncsTheDirectory(t *testing.T) {
	var synced []string
	origSyncDir := syncDirFn
	syncDirFn = func(_ afero.Fs, d string) error { synced = append(synced, d); return nil }
	defer func() { syncDirFn = origSyncDir }()

	fs := newMemDir(t, "/store")
	plain, err := NewAtomicFileIn(fs, "/store", 0o600)
	require.NoError(t, err)
	_, err = plain.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, plain.CommitAs("/store/plain"))
	assert.Empty(t, synced, "without Durable() CommitAs pays no directory sync")

	durable, err := NewAtomicFileIn(fs, "/store", 0o600, Durable())
	require.NoError(t, err)
	_, err = durable.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, durable.CommitAs("/store/durable"))
	assert.Equal(t, []string{filepath.Clean("/store")}, synced)
}

func TestAtomicFileIn_CommitAs_DurableSyncFailureFailsIt(t *testing.T) {
	boom := errors.New("device is on fire")
	origSyncDir := syncDirFn
	syncDirFn = func(afero.Fs, string) error { return boom }
	defer func() { syncDirFn = origSyncDir }()

	fs := newMemDir(t, "/store")
	af, err := NewAtomicFileIn(fs, "/store", 0o600, Durable())
	require.NoError(t, err)
	_, err = af.Write([]byte("x"))
	require.NoError(t, err)
	assert.ErrorIs(t, af.CommitAs("/store/abc"), boom)
}
