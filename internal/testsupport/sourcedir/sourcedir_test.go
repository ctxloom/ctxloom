package sourcedir_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// TestDir_IsThisPackagesSourceDirectory pins the basic claim. Asserting that a
// NAMED file is there, rather than merely that the directory exists, is what
// separates "found the package" from "found some directory": an empty temp dir
// exists too, and resolving against it is the silent-vacuity failure this
// package was written to remove.
func TestDir_IsThisPackagesSourceDirectory(t *testing.T) {
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(dir), "the package directory must be absolute, got %q", dir)
	assert.FileExists(t, filepath.Join(dir, "sourcedir.go"))
}

// TestDir_SurvivesAChdir is the property that makes this a replacement for
// runtime.Caller rather than for a bare os.Getwd. testsupport.SandboxedMain
// moves the whole process into a throwaway directory before any test runs, so
// a helper that consulted the cwd when CALLED would answer with the sandbox.
// This one answers with the package, because it captured the directory during
// package initialization, which happens before any TestMain body.
func TestDir_SurvivesAChdir(t *testing.T) {
	before, err := sourcedir.Dir()
	require.NoError(t, err)

	// Stand in for SandboxedMain's chdir: an empty directory, which is exactly
	// the shape that makes a cwd-rooted source scan pass by finding nothing.
	sandbox := t.TempDir()
	t.Chdir(sandbox)

	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NotEqual(t, before, wd, "the chdir did not take effect, so this test proves nothing")

	after, err := sourcedir.Dir()
	require.NoError(t, err)
	assert.Equal(t, before, after, "the package directory must not follow the working directory")
	assert.FileExists(t, filepath.Join(after, "sourcedir.go"))
}

// TestRepoRoot_IsTheModuleRoot proves the walk-up lands on the module root and
// not on some ancestor that merely exists.
func TestRepoRoot_IsTheModuleRoot(t *testing.T) {
	root, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, "go.mod"))

	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	rel, err := filepath.Rel(root, dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("internal", "testsupport", "sourcedir"), rel,
		"this package's path relative to the module root is the answer's own check")
}

// TestRepoRoot_SurvivesAChdir covers the module root for the same reason Dir is
// covered: RepoRoot walks up from the captured directory, so if that capture
// ever became cwd-sensitive the walk would start inside the sandbox and find
// whatever go.mod happens to sit above the OS temp root — or none.
func TestRepoRoot_SurvivesAChdir(t *testing.T) {
	before, err := sourcedir.RepoRoot()
	require.NoError(t, err)

	t.Chdir(t.TempDir())

	after, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// TestPathHelpers_JoinOntoTheRightRoots pins the two conveniences the call
// sites use most, so a future change to either root cannot quietly redirect
// every caller that goes through them.
func TestPathHelpers_JoinOntoTheRightRoots(t *testing.T) {
	assert.FileExists(t, sourcedir.Path("sourcedir.go"))
	assert.FileExists(t, sourcedir.RepoPath("go.mod"))
}
