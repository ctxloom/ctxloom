package operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// initLocalRepoWithFile creates a non-bare git repo at dir, writes filePath
// with content, commits, and returns the resulting commit SHA. Used to build
// file:// remotes for cache integration tests across the package. Moved here
// from lockfile_test.go when CheckOutdated (its original consumer) was
// deleted, since several other test files (remotes_test.go,
// search_remotes_test.go, upgrade_test.go, upgrade_erasure_test.go,
// sync_security_test.go) still need it.
func initLocalRepoWithFile(t *testing.T, dir, filePath, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))

	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	full := filepath.Join(dir, filePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))

	_, err = wt.Add(filePath)
	require.NoError(t, err)

	sha, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return sha.String()
}

// addFileToLocalRepo writes and commits an additional file into an existing
// local repo (created by initLocalRepoWithFile), returning the new commit SHA.
func addFileToLocalRepo(t *testing.T, dir, filePath, content string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	full := filepath.Join(dir, filePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))

	_, err = wt.Add(filePath)
	require.NoError(t, err)

	sha, err := wt.Commit("add "+filePath, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return sha.String()
}

// requireGit skips the test cleanly when git is unavailable, so the normal
// suite stays green on a git-less host.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping a real-git test")
	}
}

// initTestRepo creates a temp git repo with one commit and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	gitFixtureRun(t, dir, "init", "-b", "main")
	// A REPO-LOCAL identity, not just gitFixtureRun's env one. The env covers
	// only the commands this helper runs; PRODUCTION code committing into this
	// repo (git.ExecGit.CommitAll, reached via handleDirtyParentTree) shells out
	// with a SANITIZED environment and sees none of it, falling through to
	// global config — present on a developer box, absent in a container or on
	// CI, where the commit dies with "Author identity unknown".
	gitFixtureRun(t, dir, "config", "user.name", "ctxloom")
	gitFixtureRun(t, dir, "config", "user.email", "ctxloom@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed"), 0o644))
	gitFixtureRun(t, dir, "add", "README.md")
	gitFixtureRun(t, dir, "commit", "-m", "seed")
	return dir
}

func gitFixtureRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	taskstest.Git(t, dir, append(taskstest.GitIdentity("ctxloom", "ctxloom@example.com"),
		// Isolate from the developer's/CI's GLOBAL and SYSTEM git config.
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"), args...)
}
