package remote

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLockfileManager_PendingLockfile pins the pending-lockfile path that
// SyncOnStartup uses: WithPendingLockfile re-points the manager at
// lock.pending.yaml, writes land there without touching the active lock.yaml,
// and Delete drops the pending file once a review is acknowledged.
func TestLockfileManager_PendingLockfile(t *testing.T) {
	fs := afero.NewMemMapFs()
	baseDir := "/proj/.ctxloom"

	pending := NewLockfileManager(baseDir, WithLockfileFS(fs), WithPendingLockfile())
	active := NewLockfileManager(baseDir, WithLockfileFS(fs))

	assert.True(t, pending.IsPending(), "WithPendingLockfile must mark the manager as pending")
	assert.False(t, active.IsPending())
	assert.Equal(t, filepath.Join(baseDir, "lock.pending.yaml"), pending.Path())
	assert.Equal(t, filepath.Join(baseDir, "lock.yaml"), active.Path())

	lf := &Lockfile{
		Version: 1,
		Bundles: map[string]LockEntry{"github/b": {SHA: "deadbeef"}},
	}
	require.NoError(t, pending.Save(lf))

	// The write lands at the pending path only; the active lockfile is untouched.
	pendingExists, _ := afero.Exists(fs, pending.Path())
	activeExists, _ := afero.Exists(fs, active.Path())
	assert.True(t, pendingExists, "Save must write the pending lockfile")
	assert.False(t, activeExists, "the active lockfile must be left untouched")

	// The pending manager reads back what it wrote.
	loaded, err := pending.Load()
	require.NoError(t, err)
	entry, ok := loaded.GetEntry(ItemTypeBundle, "github/b")
	require.True(t, ok)
	assert.Equal(t, "deadbeef", entry.SHA)

	// Delete removes the pending file; a second Delete is a no-op.
	require.NoError(t, pending.Delete())
	stillThere, _ := afero.Exists(fs, pending.Path())
	assert.False(t, stillThere, "Delete must remove the pending lockfile")
	require.NoError(t, pending.Delete(), "Delete on a missing file must be a no-op")
}
