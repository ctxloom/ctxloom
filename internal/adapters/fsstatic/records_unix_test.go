//go:build !windows

package fsstatic

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// TestNewRecords_TightensAnExistingRecordDir: the records directory also holds
// undo records that keep the previous value of the key they undo, so it is
// owner-only. Opening the store tightens a looser existing directory on the
// REAL filesystem, before any delivery runs: an approach writing its record
// through fsstatic's copy-on-write overlay cannot chmod a directory that lives
// in the overlay's base. Opening creates nothing — `manage check` opens it too.
func TestNewRecords_TightensAnExistingRecordDir(t *testing.T) {
	fs := afero.NewOsFs()
	dir := filepath.Join(t.TempDir(), "records")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))

	_, err := NewRecords(fs, dir)
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "an existing records directory must be tightened to owner-only")
}

// TestRecords_Prepare_TightensADirLoosenedAfterOpen: Prepare, not opening,
// is what makes the store owner-only for a delivery: a directory left loose
// after the store was opened is tightened, and a missing one is not created.
func TestRecords_Prepare_TightensADirLoosenedAfterOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	r, err := NewRecords(afero.NewOsFs(), dir)
	require.NoError(t, err)

	require.NoError(t, r.Prepare(context.Background()))
	require.NoDirExists(t, dir, "preparing must not create the directory")

	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, r.Prepare(context.Background()))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestWriteThrough_CreatesAMissingDirectoryOwnerOnly: what an approach writes
// outside the target is its own state (claude's undo record), so a directory
// writeThrough has to create for it is owner-only.
func TestWriteThrough_CreatesAMissingDirectoryOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	path := filepath.Join(dir, "x.hew-record.yaml")

	require.NoError(t, writeThrough(afero.NewOsFs(), path, []byte("x"), 0o600))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestWriteThrough_LeavesAnExistingDirectoryAlone: writeThrough lands a file;
// it does not own the directory it lands in, so a directory that is already
// there keeps its mode — only one writeThrough creates is made owner-only.
func TestWriteThrough_LeavesAnExistingDirectoryAlone(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))

	require.NoError(t, writeThrough(afero.NewOsFs(), filepath.Join(dir, "x.hew-record.yaml"), []byte("x"), 0o600))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}
