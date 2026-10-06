package spool

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin that every exported spool entry point reaches
// the filesystem ONLY through the afero.Fs it is handed. Each one works
// against a MemMapFs whose files do not exist on disk: an entry point that
// still touched the OS directly would not see them, or would write beside
// them.

// memSpool returns an in-memory filesystem and the home mapper over a HOME of
// the test's own.
func memSpool(t *testing.T) (afero.Fs, PathMapper) {
	t.Helper()
	hostHome(t)
	return afero.NewMemMapFs(), NewHomeMapper()
}

// seedMem publishes one message into dir through fs, and proves it is NOT on
// the disk, so a reader that finds it can only have looked through fs.
func seedMem(t *testing.T, fs afero.Fs, m PathMapper, dir Dir, body string) Ref {
	t.Helper()
	w, err := NewWriter(fs, m, testHarp, dir, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: body})
	require.NoError(t, err)
	requireNotOnDisk(t, m, ref)
	return ref
}

// requireNotOnDisk asserts ref's file is absent from the OS filesystem.
func requireNotOnDisk(t *testing.T, m PathMapper, ref Ref) {
	t.Helper()
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "%s must exist only in the injected fs, got stat err %v", path, err)
}

func TestSweepAndRead_ReachTheDiskOnlyThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	ref := seedMem(t, fs, m, DirIn, "only in memory\n")

	res, err := Sweep(fs, m, testHarp, DirIn)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	require.Len(t, res.Entries, 1, "Sweep must list the in-memory file")
	require.Equal(t, ref, res.Entries[0].Ref)

	msg, err := Read(fs, m, ref)
	require.NoError(t, err)
	require.Equal(t, "only in memory\n", msg.Body)
}
