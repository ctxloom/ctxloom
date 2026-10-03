package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outputSession records a session "old-name" whose output dir is
// <docs>/proj/old-name, holding one file; it returns the manager, the
// sessions root and the output dir's parent.
func outputSession(t *testing.T) (*Manager, string, string) {
	t.Helper()
	m, root := openSidecarRoot(t)
	parent := filepath.Join(t.TempDir(), "proj")
	out := filepath.Join(parent, "old-name")
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "essence.md"), []byte("kept\n"), 0o644))
	writeSidecar(t, root, "old-name", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\noutput_dir: "+out+"\n")
	return m, root, parent
}

// recordedOutputDir is the output dir harp's sidecar records.
func recordedOutputDir(t *testing.T, root, harp string) string {
	t.Helper()
	out, ok := OutputDirOf(filepath.Join(root, harp))
	require.True(t, ok)
	return out
}

// Renaming a session moves its output folder to the new name, beside the old
// one, and records where it went.
func TestRename_MovesTheOutputDirAndRecordsIt(t *testing.T) {
	m, root, parent := outputSession(t)

	require.NoError(t, m.Rename("old-name", "new-name"))

	assert.NoDirExists(t, filepath.Join(parent, "old-name"))
	assert.FileExists(t, filepath.Join(parent, "new-name", "essence.md"), "the folder moved, contents and all")
	assert.Equal(t, filepath.Join(parent, "new-name"), recordedOutputDir(t, root, "new-name"))
}

// A session whose output folder was never written still renames; the record
// names the folder under the new name.
func TestRename_WithNoOutputFolderYetUpdatesTheRecord(t *testing.T) {
	m, root, parent := outputSession(t)
	require.NoError(t, os.RemoveAll(filepath.Join(parent, "old-name")))

	require.NoError(t, m.Rename("old-name", "new-name"))
	assert.Equal(t, filepath.Join(parent, "new-name"), recordedOutputDir(t, root, "new-name"))
}

// A folder already at the new name refuses the rename, and nothing moves.
func TestRename_RefusesWhenTheOutputTargetExists(t *testing.T) {
	m, root, parent := outputSession(t)
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "new-name"), 0o755))

	err := m.Rename("old-name", "new-name")
	require.ErrorIs(t, err, ErrOutputDirExists)
	assertNothingMoved(t, root, parent)
}

// A move that fails refuses the rename, and nothing moves.
func TestRename_RefusesWhenTheOutputMoveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only parent that makes the move fail")
	}
	m, root, parent := outputSession(t)
	require.NoError(t, os.Chmod(parent, 0o555))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	err := m.Rename("old-name", "new-name")
	require.ErrorIs(t, err, ErrOutputDirMove)
	assertNothingMoved(t, root, parent)
}

// assertNothingMoved: the session and its output folder are where they were,
// and the record still names the old folder.
func assertNothingMoved(t *testing.T, root, parent string) {
	t.Helper()
	assert.DirExists(t, filepath.Join(root, "old-name"))
	assert.NoDirExists(t, filepath.Join(root, "new-name"))
	assert.FileExists(t, filepath.Join(parent, "old-name", "essence.md"))
	assert.Equal(t, filepath.Join(parent, "old-name"), recordedOutputDir(t, root, "old-name"))
}
