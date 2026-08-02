package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
