package gitutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindRoot_FromRepoRoot(t *testing.T) {
	// This test runs from within the ctxloom repo
	root, err := FindRoot(".")
	require.NoError(t, err)
	assert.NotEmpty(t, root)

	// Should contain go.mod (we're in a Go project)
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	assert.NoError(t, err)
}

func TestFindRoot_FromSubdirectory(t *testing.T) {
	// Find root from a subdirectory
	root, err := FindRoot("./")
	require.NoError(t, err)

	// Try from internal subdirectory
	internalRoot, err := FindRoot(filepath.Join(root, "internal"))
	require.NoError(t, err)
	assert.Equal(t, root, internalRoot)
}

func TestFindRoot_FromFile(t *testing.T) {
	// FindRoot should work when given a file path
	root, err := FindRoot(".")
	require.NoError(t, err)

	// Pass a file instead of directory
	fileRoot, err := FindRoot(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	assert.Equal(t, root, fileRoot)
}

func TestFindRoot_NotARepo(t *testing.T) {
	// Create a temp directory that's not a git repo
	tmpDir := t.TempDir()

	_, err := FindRoot(tmpDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestFindRoot_InvalidPath(t *testing.T) {
	_, err := FindRoot("/nonexistent/path/that/does/not/exist")
	require.Error(t, err)
}

func TestFindRoot_ReturnsAbsolutePath(t *testing.T) {
	root, err := FindRoot(".")
	require.NoError(t, err)

	// Should be absolute
	assert.True(t, filepath.IsAbs(root))
}
