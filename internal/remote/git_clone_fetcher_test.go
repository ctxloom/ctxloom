package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestRepoWithFiles creates a git repo with specific ctxloom structure.
func createTestRepoWithFiles(t *testing.T, dir string) (string, string) {
	t.Helper()

	repoDir := filepath.Join(dir, "test-repo")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create directory structure
	files := map[string]string{
		"ctxloom/bundles/core.yaml":     "version: v1\ndescription: core bundle\n",
		"ctxloom/bundles/dev.yaml":      "version: v1\ndescription: dev bundle\n",
		"ctxloom/profiles/default.yaml": "bundles:\n  - core\n  - dev\n",
		"ctxloom/manifest.yaml":         "version: 1\nbundles:\n  - name: core\n  - name: dev\n",
	}

	for path, content := range files {
		fullPath := filepath.Join(repoDir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))
		_, err := wt.Add(path)
		require.NoError(t, err)
	}

	commitHash, err := wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	return repoDir, commitHash.String()
}

func TestGitCloneFetcher_FetchFile(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, sha := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	t.Run("fetch existing file", func(t *testing.T) {
		content, err := fetcher.FetchFile(context.Background(), "owner", "repo", "ctxloom/bundles/core.yaml", sha)
		require.NoError(t, err)
		assert.Contains(t, string(content), "core bundle")
	})

	t.Run("fetch with empty ref uses HEAD", func(t *testing.T) {
		content, err := fetcher.FetchFile(context.Background(), "owner", "repo", "ctxloom/bundles/core.yaml", "")
		require.NoError(t, err)
		assert.Contains(t, string(content), "core bundle")
	})

	t.Run("fetch non-existent file", func(t *testing.T) {
		_, err := fetcher.FetchFile(context.Background(), "owner", "repo", "nonexistent.yaml", sha)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestGitCloneFetcher_ListDir(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, sha := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	t.Run("list bundles directory", func(t *testing.T) {
		entries, err := fetcher.ListDir(context.Background(), "owner", "repo", "ctxloom/bundles", sha)
		require.NoError(t, err)
		assert.Len(t, entries, 2)

		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		assert.Contains(t, names, "core.yaml")
		assert.Contains(t, names, "dev.yaml")
	})

	t.Run("list non-existent directory", func(t *testing.T) {
		_, err := fetcher.ListDir(context.Background(), "owner", "repo", "nonexistent", sha)
		require.Error(t, err)
	})
}

func TestGitCloneFetcher_ResolveRef(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, commitSHA := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	t.Run("resolve full SHA", func(t *testing.T) {
		sha, err := fetcher.ResolveRef(context.Background(), "owner", "repo", commitSHA)
		require.NoError(t, err)
		assert.Equal(t, commitSHA, sha)
	})

	t.Run("resolve abbreviated SHA", func(t *testing.T) {
		sha, err := fetcher.ResolveRef(context.Background(), "owner", "repo", commitSHA[:7])
		require.NoError(t, err)
		assert.Equal(t, commitSHA, sha)
	})

	t.Run("resolve non-existent ref", func(t *testing.T) {
		_, err := fetcher.ResolveRef(context.Background(), "owner", "repo", "nonexistent-branch")
		require.Error(t, err)
	})
}

// TestGitCloneFetcher_ResolveRef_BranchTracksOrigin pins the resolution-order
// fix: a branch name must resolve to the freshly-fetched remote-tracking tip
// (refs/remotes/origin/<branch>), not the stale clone-time local branch
// (refs/heads/<branch>). git fetch advances only the remote-tracking refs, and
// go-git's bare ResolveRevision walks refs/heads first, so resolving a branch
// name bare returned the clone-time commit. The branch is named with >=7 chars
// ("develop") because the old code only mis-resolved refs in that length band.
func TestGitCloneFetcher_ResolveRef_BranchTracksOrigin(t *testing.T) {
	tmpDir := t.TempDir()

	const branch = "develop"
	branchRef := plumbing.NewBranchReferenceName(branch)

	// Origin repo whose default branch is "develop".
	originDir := filepath.Join(tmpDir, "origin")
	origin, err := git.PlainInit(originDir, false)
	require.NoError(t, err)
	require.NoError(t, origin.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, branchRef)))
	originWT, err := origin.Worktree()
	require.NoError(t, err)
	commit := func(wt *git.Worktree, dir, content string) plumbing.Hash {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte(content), 0o644))
		_, err := wt.Add("file.txt")
		require.NoError(t, err)
		h, err := wt.Commit(content, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t.com", When: time.Now()},
		})
		require.NoError(t, err)
		return h
	}
	oldSHA := commit(originWT, originDir, "first")

	// Clone it: the clone gets refs/heads/develop AND refs/remotes/origin/develop
	// both at oldSHA.
	cloneDir := filepath.Join(tmpDir, "clone")
	clone, err := git.PlainClone(cloneDir, false, &git.CloneOptions{URL: "file://" + originDir})
	require.NoError(t, err)

	// Advance origin's develop to a new commit.
	newSHA := commit(originWT, originDir, "second")
	require.NotEqual(t, oldSHA, newSHA)

	// Fetch into the clone: advances refs/remotes/origin/* only, leaving the
	// local refs/heads/develop at oldSHA — exactly the cache-clone state.
	err = clone.Fetch(&git.FetchOptions{
		RefSpecs: []gitconfig.RefSpec{"+refs/heads/*:refs/remotes/origin/*"},
		Force:    true,
	})
	require.NoError(t, err)

	fetcher, err := NewGitCloneFetcher(cloneDir, "file://"+originDir, ForgeGitHub, nil)
	require.NoError(t, err)

	got, err := fetcher.ResolveRef(context.Background(), "owner", "repo", branch)
	require.NoError(t, err)
	assert.Equal(t, newSHA.String(), got, "branch should resolve to the fetched origin tip, not the stale local branch")
}

func TestGitCloneFetcher_ValidateRepo(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, _ := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	valid, err := fetcher.ValidateRepo(context.Background(), "owner", "repo")
	require.NoError(t, err)
	assert.True(t, valid)
}

func TestGitCloneFetcher_GetDefaultBranch(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, _ := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	branch, err := fetcher.GetDefaultBranch(context.Background(), "owner", "repo")
	require.NoError(t, err)
	// Default branch for git init is typically "master" or configured default
	assert.NotEmpty(t, branch)
}

func TestGitCloneFetcher_Forge(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, _ := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)
	assert.Equal(t, ForgeGitHub, fetcher.Forge())

	fetcher2, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitGeneric, nil)
	require.NoError(t, err)
	assert.Equal(t, ForgeGitGeneric, fetcher2.Forge())
}

func TestGitCloneFetcher_SearchRepos(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, _ := createTestRepoWithFiles(t, tmpDir)

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	_, err = fetcher.SearchRepos(context.Background(), "test", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}
