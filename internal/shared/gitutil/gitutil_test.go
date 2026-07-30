package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
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

// TestGetRemoteURL_SCPSyntax pins that an scp-style origin round-trips
// verbatim (folded in from the retired GetOriginURL wrapper's test).
func TestGetRemoteURL_SCPSyntax(t *testing.T) {
	repo := initTempRepo(t)
	addRemote(t, repo, "origin", "git@github.com:example/repo.git")

	url, err := GetRemoteURL(repo, "origin")
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:example/repo.git", url)
}

// TestGetRemoteURL_MissingOrigin pins the "origin" caller's failure mode: a
// repo with only a non-origin remote errors (folded in from the retired
// GetOriginURL wrapper's test).
func TestGetRemoteURL_MissingOrigin(t *testing.T) {
	repo := initTempRepo(t)
	addRemote(t, repo, "fork", "https://github.com/me/fork.git")

	_, err := GetRemoteURL(repo, "origin")
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

// TestGetRemoteURL_FromLinkedWorktree pins U110-F01: PlainOpenOptions was
// constructed with DetectDotGit true but EnableDotGitCommonDir left false. In
// a linked git worktree (this project's own standard agent workflow), go-git
// then resolves the ".git" FILE's `gitdir:` pointer directly as the repo
// filesystem — a private per-worktree admin dir containing no `config` (that
// lives only in the common dir) — so `remote "origin" not configured` fires
// in EVERY linked worktree even though the origin genuinely exists.
func TestGetRemoteURL_FromLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping linked-worktree integration test")
	}
	main, linked := taskstest.RealGitWorktreeFixture(t)
	cmd := exec.Command("git", "remote", "add", "origin", "https://example.com/repo.git")
	cmd.Dir = main
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	url, err := GetRemoteURL(linked, "origin")
	require.NoError(t, err, "origin IS configured — in the shared common dir a linked worktree must resolve through")
	assert.Equal(t, "https://example.com/repo.git", url)
}

// SHA abbreviation had grown three separate copies of this three-line rule at
// two different widths. The widths are a deliberate difference — ordinary
// output takes git's 7, a prompt's evidence lines take a longer prefix — so
// the width is a parameter and the RULE is shared. Both widths are pinned here
// because both are live output.
func TestAbbrevSHA_TruncatesAtTheRequestedWidth(t *testing.T) {
	assert.Equal(t, "abc1234", ShortSHA("abc12345def"), "the default width is git's 7")
	assert.Equal(t, "abcdef1234", AbbrevSHA("abcdef1234567890", 10))
}

func TestAbbrevSHA_NeverSlicesPastTheEnd(t *testing.T) {
	assert.Equal(t, "abc", ShortSHA("abc"), "a short hash is returned as-is")
	assert.Equal(t, "abc1234", ShortSHA("abc1234"), "exactly the width hits len > n on the false side")
	assert.Equal(t, "", ShortSHA(""))
	assert.Equal(t, "abc", AbbrevSHA("abc", 10))
}
