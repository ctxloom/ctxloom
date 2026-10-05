package remote

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// writeCommit applies a set of writes and deletions to the non-bare repo at dir
// and commits them, so a test can build up history with files that come and go.
func writeCommit(t *testing.T, dir, msg string, writes map[string]string, removes []string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	for p, content := range writes {
		full := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
		_, err = wt.Add(p)
		require.NoError(t, err)
	}
	for _, p := range removes {
		require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(p))))
		_, err = wt.Add(p) // stage the deletion
		require.NoError(t, err)
	}
	_, err = wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	return dir
}
