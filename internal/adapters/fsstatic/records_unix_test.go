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

// TestRecords_PrepareCreatesNothing: opening the store and preparing it
// create no directory — `manage check` opens it too. A delivery's writes
// create what they need owner-only.
func TestRecords_PrepareCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	r, err := NewRecords(afero.NewOsFs(), dir)
	require.NoError(t, err)

	require.NoError(t, r.Prepare(context.Background()))
	require.NoDirExists(t, dir, "preparing must not create the directory")
}

// TestWriteThrough_CreatesAMissingDirectoryOwnerOnly: what an approach writes
// outside the target is its own state, so a directory writeThrough has to
// create for it is owner-only.
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
