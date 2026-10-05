package tasks

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Reading a log that does not exist yet is a read of nothing: it takes no
// lock, so it creates neither the tasks directory nor the .lock file. A log
// only ever comes into being under the write lock, so a read that saw no log
// is ordered before that write and has nothing to wait for.
func TestStore_ReadingAnAbsentLogCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks")
	logPath := filepath.Join(dir, "vain-void-charm.jsonl")
	s, err := OpenLog(logPath, "")
	require.NoError(t, err)

	list, err := s.List(nil, "")
	require.NoError(t, err)
	require.Empty(t, list)
	snap, err := s.Snapshot()
	require.NoError(t, err)
	require.Empty(t, snap)
	since, err := s.DeferredSince()
	require.NoError(t, err)
	require.Empty(t, since)
	require.NoDirExists(t, dir, "a read of an absent log must not create the tasks directory")

	// Hostile half: the first write lays out the log and its lock.
	_, err = s.AddWithTrigger("first", StatusToDo, "")
	require.NoError(t, err)
	require.FileExists(t, logPath)
	require.FileExists(t, paths.PathFor(logPath))
}
