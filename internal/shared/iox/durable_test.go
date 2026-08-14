package iox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDurable_SetsConfigFlag pins the option-plumbing seam directly: removing
// Durable's body (or forgetting to read cfg.durable downstream) is caught
// here without needing the filesystem at all. The zero option set — no
// caller opted in — must leave durable false, matching every entry point's
// historical default.
func TestDurable_SetsConfigFlag(t *testing.T) {
	assert.True(t, resolveOptions([]Option{Durable()}).durable,
		"Durable() must set the durable config flag")
	assert.False(t, resolveOptions(nil).durable,
		"no options passed must leave durable false")
	assert.False(t, resolveOptions([]Option{AllowEmpty()}).durable,
		"an unrelated option must not turn durable on")
}

// TestWriteFileAtomicFs_Durable_NoOpOnNonOSBackedFs pins the afero escape
// hatch Durable's doc promises: a directory fsync needs a real fd, which
// MemMapFs cannot hand back, so opting in must not touch the seam at all
// (never mind fail) on a test double — existing MemMapFs-backed callers stay
// unaffected by adding Durable() anywhere in their write path.
func TestWriteFileAtomicFs_Durable_NoOpOnNonOSBackedFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/dir", 0o755))

	var syncCalled bool
	origSyncDir := syncDirFn
	syncDirFn = func(string) error { syncCalled = true; return nil }
	defer func() { syncDirFn = origSyncDir }()

	require.NoError(t, WriteFileAtomicFs(fs, "/dir/out.yaml", []byte("hello"), 0o644, Durable()))
	assert.False(t, syncCalled, "Durable() must not touch the sync seam on a non-OS-backed filesystem")

	data, err := afero.ReadFile(fs, "/dir/out.yaml")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

// TestWriteFileAtomicFs_Durable_DirSyncFailureFailsTheWrite pins that a
// caller who asked for Durable() and did not get it is TOLD, exactly the
// countersign/artifactstore reasoning: a receipt asserting a guarantee that
// was not actually bought is worse than an error, because nothing about the
// caller's later behavior would ever reveal the gap. The rename has already
// landed by the time the sync runs, so the earlier bytes are still replaced —
// this failure reports "not durably named yet", not "write failed".
func TestWriteFileAtomicFs_Durable_DirSyncFailureFailsTheWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.yaml")

	boom := errors.New("device is on fire")
	origSyncDir := syncDirFn
	syncDirFn = func(string) error { return boom }
	defer func() { syncDirFn = origSyncDir }()

	err := WriteFileAtomicFs(afero.NewOsFs(), target, []byte("hello"), 0o644, Durable())
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)

	got, rerr := os.ReadFile(target)
	require.NoError(t, rerr, "the rename already landed; a durability failure must not be mistaken for a missing file")
	assert.Equal(t, "hello", string(got))
}

// TestWriteFileAtomic_Durable_SyncsParentDirectory exercises the OS entry
// point directly (not just through the parity table), pinning that Durable()
// reaches syncDirFn with exactly the write's own parent directory.
func TestWriteFileAtomic_Durable_SyncsParentDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.yaml")

	var synced []string
	origSyncDir := syncDirFn
	syncDirFn = func(d string) error { synced = append(synced, d); return nil }
	defer func() { syncDirFn = origSyncDir }()

	require.NoError(t, WriteFileAtomic(target, []byte("hello"), 0o644, Durable()))
	assert.Equal(t, []string{dir}, synced)
}

// TestAtomicFile_Commit_Durable_SyncsParentDirectory pins the streaming
// counterpart: Commit must run the same durable-upgrade step
// WriteFileAtomicFs's rename does, not a hand-copied divergent one.
func TestAtomicFile_Commit_Durable_SyncsParentDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.yaml")

	var synced []string
	origSyncDir := syncDirFn
	syncDirFn = func(d string) error { synced = append(synced, d); return nil }
	defer func() { syncDirFn = origSyncDir }()

	af, err := NewAtomicFile(target, 0o644, Durable())
	require.NoError(t, err)
	_, err = af.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, af.Commit())

	assert.Equal(t, []string{dir}, synced)
}

// TestAtomicFile_Commit_WithoutDurable_DoesNotSyncParentDirectory is the
// characterization half: an AtomicFile that never opted in must not pay the
// extra fsync, and the seam must not fire on its own.
func TestAtomicFile_Commit_WithoutDurable_DoesNotSyncParentDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.yaml")

	var synced []string
	origSyncDir := syncDirFn
	syncDirFn = func(d string) error { synced = append(synced, d); return nil }
	defer func() { syncDirFn = origSyncDir }()

	af, err := NewAtomicFile(target, 0o644)
	require.NoError(t, err)
	_, err = af.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, af.Commit())

	assert.Empty(t, synced)
}

// TestAtomicFile_Commit_Durable_DirSyncFailureFailsCommit mirrors the
// whole-file writer's failure-propagation test for the streaming entry
// point.
func TestAtomicFile_Commit_Durable_DirSyncFailureFailsCommit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.yaml")

	boom := errors.New("device is on fire")
	origSyncDir := syncDirFn
	syncDirFn = func(string) error { return boom }
	defer func() { syncDirFn = origSyncDir }()

	af, err := NewAtomicFile(target, 0o644, Durable())
	require.NoError(t, err)
	_, err = af.Write([]byte("hello"))
	require.NoError(t, err)

	err = af.Commit()
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)

	got, rerr := os.ReadFile(target)
	require.NoError(t, rerr, "the rename already landed before the dir sync ran")
	assert.Equal(t, "hello", string(got))
}
