package safefs_test

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// recordDirSyncs swaps the directory-sync hook for one that records every
// directory it is asked to sync, returning the record.
func recordDirSyncs(t *testing.T) *[]string {
	t.Helper()
	var synced []string
	restore := safefs.SetSyncDirForTesting(func(dir string) error {
		synced = append(synced, dir)
		return nil
	})
	t.Cleanup(restore)
	return &synced
}

func TestDurableFs_RenameSyncsBothParents(t *testing.T) {
	synced := recordDirSyncs(t)
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/a", 0o755))
	require.NoError(t, base.MkdirAll("/b", 0o755))
	require.NoError(t, afero.WriteFile(base, "/a/f", []byte("x"), 0o644))

	require.NoError(t, safefs.NewDurableFs(base).Rename("/a/f", "/b/f"))
	assert.Equal(t, []string{"/b", "/a"}, *synced)
}

func TestDurableFs_RenameWithinOneDirSyncsItOnce(t *testing.T) {
	synced := recordDirSyncs(t)
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/a/f", []byte("x"), 0o644))

	require.NoError(t, safefs.NewDurableFs(base).Rename("/a/f", "/a/g"))
	assert.Equal(t, []string{"/a"}, *synced)
}

func TestDurableFs_FailedRenameSyncsNothing(t *testing.T) {
	synced := recordDirSyncs(t)
	err := safefs.NewDurableFs(afero.NewMemMapFs()).Rename("/missing", "/a/g")
	require.Error(t, err)
	assert.Empty(t, *synced)
}

// The rename is visible by the time the sync fails, but the caller asked for
// a durable name, so the failure is still the caller's to see.
func TestDurableFs_DirSyncFailureIsReturned(t *testing.T) {
	boom := errors.New("boom")
	restore := safefs.SetSyncDirForTesting(func(string) error { return boom })
	t.Cleanup(restore)
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/a/f", []byte("x"), 0o644))

	require.ErrorIs(t, safefs.NewDurableFs(base).Rename("/a/f", "/a/g"), boom)
}

func TestDurableFs_CreatedFileSyncsItsParentOnClose(t *testing.T) {
	synced := recordDirSyncs(t)
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/a", 0o755))

	f, err := safefs.NewDurableFs(base).OpenFile("/a/new", os.O_WRONLY|os.O_CREATE, 0o644)
	require.NoError(t, err)
	assert.Empty(t, *synced, "nothing is synced before Close")
	require.NoError(t, f.Close())
	assert.Equal(t, []string{"/a"}, *synced)
}

// Writing into a file that already existed adds no directory entry, so there
// is no name to make durable.
func TestDurableFs_ExistingFileSyncsNoDirectory(t *testing.T) {
	synced := recordDirSyncs(t)
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/a/old", []byte("x"), 0o644))

	f, err := safefs.NewDurableFs(base).OpenFile("/a/old", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Empty(t, *synced)
}

func TestDurableFs_ReadOnlyOpenIsUntouched(t *testing.T) {
	synced := recordDirSyncs(t)
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/a/old", []byte("x"), 0o644))

	f, err := safefs.NewDurableFs(base).Open("/a/old")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Empty(t, *synced)
}

// A read-only open has nothing to make durable: it gets the base's own file,
// not a wrapper that would fsync on Close.
func TestDurableFs_ReadOnlyOpenIsTheBaseFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f"
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	f, err := safefs.NewDurableFs(afero.NewOsFs()).OpenFile(path, os.O_RDONLY, 0)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	_, isOS := f.(*os.File)
	assert.True(t, isOS, "a read-only open must not be wrapped")
}
