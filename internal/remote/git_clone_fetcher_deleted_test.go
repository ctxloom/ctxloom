package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
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

func TestGitCloneFetcher_ListDeletedItems(t *testing.T) {
	dir := newTestRepo(t)
	writeCommit(t, dir, "add foo+bar+nested", map[string]string{
		"ctxloom/bundles/foo.yaml":         "x",
		"ctxloom/bundles/bar.yaml":         "x",
		"ctxloom/bundles/nested/keep.yaml": "x",
	}, nil)
	writeCommit(t, dir, "remove bar + nested/keep", nil, []string{
		"ctxloom/bundles/bar.yaml",
		"ctxloom/bundles/nested/keep.yaml",
	})

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)

	deleted, err := f.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Equal(t, []string{"bar", "nested/keep"}, deleted, "removed-upstream items, nested paths kept relative")

	// foo survives at HEAD and so must NOT appear as deleted; it lists as present.
	items, err := (&gitForgeVCS{fetcher: f, owner: "o", repo: "r"}).ListItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Equal(t, []string{"foo"}, items)
}

func TestGitCloneFetcher_ListDeletedItems_ReAddedIsPresent(t *testing.T) {
	dir := newTestRepo(t)
	writeCommit(t, dir, "add", map[string]string{"ctxloom/bundles/foo.yaml": "x"}, nil)
	writeCommit(t, dir, "remove", nil, []string{"ctxloom/bundles/foo.yaml"})
	writeCommit(t, dir, "re-add", map[string]string{"ctxloom/bundles/foo.yaml": "y"}, nil)

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)

	deleted, err := f.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, deleted, "an item re-added at HEAD is present, not deleted")
}

func TestGitForgeVCS_ListDeletedItems_NoHistoryBackend(t *testing.T) {
	// The forge-API mock has no history capability, so deletions cannot be known
	// and the result is empty (not an error).
	vcs := &gitForgeVCS{fetcher: NewMockFetcher(), owner: "o", repo: "r"}
	deleted, err := vcs.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, deleted)
}
