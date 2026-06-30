package gitutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
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
	// FindRoot from a nested subdirectory resolves to the same repo root.
	// Hermetic (temp repo) so it doesn't assume any particular dir layout.
	repo := initTempRepo(t)
	sub := filepath.Join(repo, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	rootFromSub, err := FindRoot(sub)
	require.NoError(t, err)
	rootFromTop, err := FindRoot(repo)
	require.NoError(t, err)
	assert.Equal(t, rootFromTop, rootFromSub)
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

// initTempRepo creates a fresh git repo in a tempdir and returns its path.
// Used by the Get*URL tests so we don't depend on whatever remotes
// happen to be configured on the host running the test.
func initTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	return dir
}

func addRemote(t *testing.T, repoDir, name, url string) {
	t.Helper()
	repo, err := git.PlainOpen(repoDir)
	require.NoError(t, err)
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: name,
		URLs: []string{url},
	})
	require.NoError(t, err)
}

func TestGetRemoteURL_Found(t *testing.T) {
	repo := initTempRepo(t)
	addRemote(t, repo, "origin", "https://github.com/example/repo.git")

	url, err := GetRemoteURL(repo, "origin")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/example/repo.git", url)
}

func TestGetRemoteURL_UnknownRemote(t *testing.T) {
	repo := initTempRepo(t)
	// Only an "origin" remote; asking for "upstream" should error.
	addRemote(t, repo, "origin", "https://github.com/example/repo.git")

	_, err := GetRemoteURL(repo, "upstream")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"upstream" not configured`)
}

func TestGetRemoteURL_NotARepo(t *testing.T) {
	dir := t.TempDir() // no git init
	_, err := GetRemoteURL(dir, "origin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestGetOriginURL_DelegatesToOrigin(t *testing.T) {
	repo := initTempRepo(t)
	addRemote(t, repo, "origin", "git@github.com:example/repo.git")

	url, err := GetOriginURL(repo)
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:example/repo.git", url)
}

func TestGetOriginURL_MissingOrigin(t *testing.T) {
	repo := initTempRepo(t)
	// Add only a non-origin remote.
	addRemote(t, repo, "fork", "https://github.com/me/fork.git")

	_, err := GetOriginURL(repo)
	require.Error(t, err)
}

func TestGetRemoteURL_FilePathResolvesToRepo(t *testing.T) {
	// Passing a file path should resolve to the file's directory.
	repo := initTempRepo(t)
	addRemote(t, repo, "origin", "https://github.com/example/repo.git")
	fileInRepo := filepath.Join(repo, "README.md")
	require.NoError(t, os.WriteFile(fileInRepo, []byte("hi"), 0o644))

	url, err := GetRemoteURL(fileInRepo, "origin")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/example/repo.git", url)
}
