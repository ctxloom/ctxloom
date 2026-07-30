package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
		".ctxloom/content/bundles/foo.yaml":         "x",
		".ctxloom/content/bundles/bar.yaml":         "x",
		".ctxloom/content/bundles/nested/keep.yaml": "x",
	}, nil)
	writeCommit(t, dir, "remove bar + nested/keep", nil, []string{
		".ctxloom/content/bundles/bar.yaml",
		".ctxloom/content/bundles/nested/keep.yaml",
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
	writeCommit(t, dir, "add", map[string]string{".ctxloom/content/bundles/foo.yaml": "x"}, nil)
	writeCommit(t, dir, "remove", nil, []string{".ctxloom/content/bundles/foo.yaml"})
	writeCommit(t, dir, "re-add", map[string]string{".ctxloom/content/bundles/foo.yaml": "y"}, nil)

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)

	deleted, err := f.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, deleted, "an item re-added at HEAD is present, not deleted")
}

// TestGitCloneFetcher_ListDeletedItems_HeadUnreadable pins U093-F03: a failure
// to read the HEAD tree used to be swallowed with `if err == nil`, leaving the
// present-at-HEAD baseline EMPTY — so every item ever seen anywhere in history
// came back as "removed upstream", which is the input to a prune.
//
// The corruption is the realistic one: refs/remotes/origin/<default> resolves
// to a commit whose tree object is gone (a pruned or partially-fetched clone).
// treeAtRef("") prefers that remote-tracking ref, so it fails — while
// repo.Log(), which reads from HEAD, keeps working and happily enumerates the
// whole history. That asymmetry is exactly what turned a read failure into a
// deletion report instead of an error.
func TestGitCloneFetcher_ListDeletedItems_HeadUnreadable(t *testing.T) {
	dir := newTestRepo(t)
	writeCommit(t, dir, "add alpha", map[string]string{".ctxloom/content/bundles/alpha.yaml": "x"}, nil)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	firstCommit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)

	writeCommit(t, dir, "add beta", map[string]string{".ctxloom/content/bundles/beta.yaml": "x"}, nil)

	// Point the remote-tracking ref at the FIRST commit, then remove that
	// commit's root tree object. HEAD and its own tree stay intact.
	defaultBranch := head.Name().Short()
	require.NoError(t, repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewRemoteReferenceName("origin", defaultBranch), firstCommit.Hash)))
	treeHash := firstCommit.TreeHash.String()
	require.NoError(t, os.Remove(filepath.Join(dir, ".git", "objects", treeHash[:2], treeHash[2:])))

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)

	deleted, err := f.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.Error(t, err, "an unreadable HEAD tree must be reported, never treated as an empty baseline")
	assert.Empty(t, deleted, "no item may be reported as removed upstream on the strength of a read that failed")
}

func TestGitForgeVCS_ListDeletedItems_NoHistoryBackend(t *testing.T) {
	// The forge-API mock has no history capability, so deletions cannot be known
	// and the result is empty (not an error).
	vcs := &gitForgeVCS{fetcher: NewMockFetcher(), owner: "o", repo: "r"}
	deleted, err := vcs.ListDeletedItems(context.Background(), ItemTypeBundle)
	require.NoError(t, err)
	assert.Empty(t, deleted)
}
