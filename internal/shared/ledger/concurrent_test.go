package ledger

import (
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// lockIsFree reports whether nobody holds the advisory lock guarding target,
// probing it the way a second process would: a separate open file description
// and a non-blocking flock. It is the adversarial scheduler's question — "may
// another writer run right now?" — answered without waiting for anything.
func lockIsFree(t *testing.T, target string) bool {
	t.Helper()
	lockPath, err := paths.HomePathFor(target)
	require.NoError(t, err)
	require.NoError(t, filelock.Prepare(lockPath))
	probe := flock.New(lockPath)
	free, err := probe.TryLock()
	require.NoError(t, err)
	if free {
		require.NoError(t, probe.Unlock())
	}
	return free
}

// TestLedger_Write_ExcludesACoLocatedWriterFromItsWindow forces the lost
// update this package's co-location invariant exists to prevent: writer A has
// read the marker and not yet renamed its rewrite into place, and writer B —
// a DIFFERENT surface in the SAME directory, holding whatever lock its own
// caller takes (a different settings file, or none) — writes in between. A
// then renames a marker built from its stale read, and B's surface is gone.
//
// The interleaving is forced, never waited for. Write warns on a net
// retraction from INSIDE its read-modify-write window, so A's Warn is the
// point where an adversarial scheduler slips B in — and it does so whenever
// the marker's lock lets a second writer through. Unserialized, B lands in the
// window and is lost. Serialized, B is excluded until A's rename has landed,
// runs after it, and nothing is lost.
func TestLedger_Write_ExcludesACoLocatedWriterFromItsWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	dir := t.TempDir()
	marker := filepath.Join(dir, Name)

	// A owns "old", so its next Write is a retraction and warns mid-window.
	require.NoError(t, Ledger{FS: fs, Dir: dir}.Write(SurfaceCommands, []string{"old"}))

	writeB := func() error { return Ledger{FS: fs, Dir: dir}.Write(SurfaceSkills, []string{"b"}) }
	bInWindow := false
	a := Ledger{FS: fs, Dir: dir, Warn: func(string, ...any) {
		if !lockIsFree(t, marker) {
			return
		}
		bInWindow = true
		require.NoError(t, writeB())
	}}

	require.NoError(t, a.Write(SurfaceCommands, []string{"new"}))
	if !bInWindow {
		require.NoError(t, writeB())
	}

	read := Ledger{FS: fs, Dir: dir}
	skills, err := read.Read(SurfaceSkills)
	require.NoError(t, err)
	commands, err := read.Read(SurfaceCommands)
	require.NoError(t, err)
	assert.False(t, bInWindow, "a co-located writer got inside another's read-modify-write window: the marker is not locked across it")
	assert.Equal(t, []string{"b"}, skills, "the co-located surface's write was lost")
	assert.Equal(t, []string{"new"}, commands)
}
