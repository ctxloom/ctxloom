package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// restoreBuildState snapshots the package-level build state and puts it back
// when the test ends. Without it a teardown test would clear binDir and so
// re-leak the real build directory that TestMain's removeTasksBinary is
// supposed to delete -- the exact bug under test, reintroduced by its own
// test. No test in this package calls t.Parallel, so serial mutation of these
// vars is safe.
func restoreBuildState(t *testing.T) {
	t.Helper()
	dir, path, err := binDir, binPath, buildErr
	t.Cleanup(func() { binDir, binPath, buildErr = dir, path, err })
}

// TestRemoveTasksBinary_DeletesTheBuildDirectory asserts the DIRECTORY IS
// GONE, not that a remover ran: the leak this fixes was 161 orphaned build
// dirs / 4.5G, and a teardown that merely forgets the path leaks exactly as
// much as no teardown at all. See task smashing-olive.
func TestRemoveTasksBinary_DeletesTheBuildDirectory(t *testing.T) {
	restoreBuildState(t)

	dir := filepath.Join(t.TempDir(), "tasks-bin-123")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	bin := filepath.Join(dir, "tasks")
	// Non-empty payload: an assertion against an empty directory would pass
	// for a remover that only unlinked the leaf file.
	require.NoError(t, os.WriteFile(bin, []byte("not really a 30MB binary"), 0o755))

	binDir, binPath, buildErr = dir, bin, nil

	removeTasksBinary()

	require.NoDirExists(t, dir, "teardown must unlink the build directory, not merely forget its path")
	require.Empty(t, binDir, "teardown must clear the recorded directory so a repeat call is a no-op")
}

// TestRemoveTasksBinary_IsIdempotent covers the double-teardown path: a second
// call must neither panic nor start deleting something else.
func TestRemoveTasksBinary_IsIdempotent(t *testing.T) {
	restoreBuildState(t)

	parent := t.TempDir()
	dir := filepath.Join(parent, "tasks-bin-456")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	binDir, binPath, buildErr = dir, filepath.Join(dir, "tasks"), nil

	removeTasksBinary()
	removeTasksBinary()

	require.NoDirExists(t, dir)
	require.DirExists(t, parent, "a repeat call must not walk up and delete the parent")
}

// TestRemoveTasksBinary_WithNoBuildRecordedTouchesNothing pins the
// filepath.Dir("") == "." hazard: deriving the directory from an unset binary
// path would make an un-built teardown RemoveAll the working directory, which
// under `go test` is this package's source directory.
func TestRemoveTasksBinary_WithNoBuildRecordedTouchesNothing(t *testing.T) {
	restoreBuildState(t)

	wd, err := os.Getwd()
	require.NoError(t, err)
	binDir, binPath, buildErr = "", "", nil

	removeTasksBinary()

	require.FileExists(t, filepath.Join(wd, "crossprocess_test.go"),
		"teardown before any build must not delete the working directory")
}
