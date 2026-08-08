//go:build integration || acceptance

package testenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// restoreTaskloomBuildState snapshots the package-level build state and puts it
// back when the test ends, so a teardown test cannot make the real binary
// (built once per process, shared by every scenario) unreachable — or, worse,
// silently re-leak it by clearing the directory the real teardown would remove.
func restoreTaskloomBuildState(t *testing.T) {
	t.Helper()
	dir, path, buildErr := taskloomBinDir, taskloomBinPath, taskloomBuildErr
	t.Cleanup(func() {
		taskloomBinDir, taskloomBinPath, taskloomBuildErr = dir, path, buildErr
	})
}

// TestRemoveTaskloomBinary_DeletesTheBuildDirectory asserts the DIRECTORY IS
// GONE, not that a remover was called: the leak this fixes was 304 orphaned
// build dirs / 8.5G, and a teardown that merely forgets the path leaks exactly
// as much as no teardown at all. See task smashing-olive.
func TestRemoveTaskloomBinary_DeletesTheBuildDirectory(t *testing.T) {
	restoreTaskloomBuildState(t)

	dir := filepath.Join(t.TempDir(), "taskloom-bin-123")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	bin := filepath.Join(dir, "taskloom")
	// Non-empty payload: RemoveAll must clear the contents too, and an
	// assertion on an empty directory would pass against a broken remover
	// that only unlinked the leaf.
	require.NoError(t, os.WriteFile(bin, []byte("not really a 30MB binary"), 0o755))

	taskloomBinDir, taskloomBinPath, taskloomBuildErr = dir, bin, nil

	RemoveTaskloomBinary()

	require.NoDirExists(t, dir, "teardown must unlink the build directory, not merely forget its path")
	require.Empty(t, taskloomBinDir, "teardown must clear the recorded directory so a second call is a no-op")
}

// TestRemoveTaskloomBinary_IsIdempotent covers the double-teardown path: a
// second call must neither panic nor start deleting something else.
func TestRemoveTaskloomBinary_IsIdempotent(t *testing.T) {
	restoreTaskloomBuildState(t)

	parent := t.TempDir()
	dir := filepath.Join(parent, "taskloom-bin-456")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	taskloomBinDir, taskloomBinPath, taskloomBuildErr = dir, filepath.Join(dir, "taskloom"), nil

	RemoveTaskloomBinary()
	RemoveTaskloomBinary()

	require.NoDirExists(t, dir)
	require.DirExists(t, parent, "a repeat call must not walk up and delete the parent")
}

// TestRemoveTaskloomBinary_WithNoBuildRecordedTouchesNothing pins the
// filepath.Dir("") == "." hazard: deriving the directory from an unset binary
// path would make an un-built teardown RemoveAll the process's working
// directory — which, for `go test`, is the package source directory.
func TestRemoveTaskloomBinary_WithNoBuildRecordedTouchesNothing(t *testing.T) {
	restoreTaskloomBuildState(t)

	wd, err := os.Getwd()
	require.NoError(t, err)
	taskloomBinDir, taskloomBinPath, taskloomBuildErr = "", "", nil

	RemoveTaskloomBinary()

	require.FileExists(t, filepath.Join(wd, "taskloom_acceptance.go"),
		"teardown before any build must not delete the working directory")
}
