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
		".ctxloom/content/bundles/core.yaml":     "version: v1\ndescription: core bundle\n",
		".ctxloom/content/bundles/dev.yaml":      "version: v1\ndescription: dev bundle\n",
		".ctxloom/content/profiles/default.yaml": "bundles:\n  - core\n  - dev\n",
		".ctxloom/content/manifest.yaml":         "version: 1\nbundles:\n  - name: core\n  - name: dev\n",
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
		content, err := fetcher.FetchFile(context.Background(), "owner", "repo", ".ctxloom/content/bundles/core.yaml", sha)
		require.NoError(t, err)
		assert.Contains(t, string(content), "core bundle")
	})

	t.Run("fetch with empty ref uses HEAD", func(t *testing.T) {
		content, err := fetcher.FetchFile(context.Background(), "owner", "repo", ".ctxloom/content/bundles/core.yaml", "")
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
		entries, err := fetcher.ListDir(context.Background(), "owner", "repo", ".ctxloom/content/bundles", sha)
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

// TestGitCloneFetcher_GetDefaultBranch_UnresolvableIsAnError pins U093-F27: the
// last arm used to return the literal "main" with a nil error, so a guess
// arrived at the caller wearing the same clothes as an answer — and the callers
// are `ctxloom publish` (which branch to commit to) and the retraction reader
// (which branch to read the manifest from), where being wrong is silent and
// consequential.
//
// The exhausted case is real: a detached HEAD with no remote-tracking refs, the
// shape a bare fixture or a checkout-by-SHA leaves behind. There is nothing
// further to try, and "I could not determine it" is the true answer.
func TestGitCloneFetcher_GetDefaultBranch_UnresolvableIsAnError(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir, _ := createTestRepoWithFiles(t, tmpDir)

	repo, err := git.PlainOpen(repoDir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	// Detach HEAD onto the same commit; no origin/* refs exist in this fixture.
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.HEAD, head.Hash())))

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	branch, err := fetcher.GetDefaultBranch(context.Background(), "owner", "repo")
	require.Error(t, err, "a guess must not be returned as an answer")
	assert.Empty(t, branch)

	// treeAtRef's empty-ref path owns this failure and must still resolve
	// through the local HEAD, so an unknown default branch never costs a read.
	data, err := fetcher.FetchFile(context.Background(), "owner", "repo", ".ctxloom/content/bundles/core.yaml", "")
	require.NoError(t, err)
	assert.NotEmpty(t, data)
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

// TestGitCloneFetcher_ResolveTag_TagWinsOverSameNamedBranch pins the semver
// pinning security fix: when upstream carries BOTH a tag and a branch named
// "v1.0.0", the generic ResolveRef resolves the branch (origin/<ref> is tried
// first), so a lock built from it would silently track the branch tip.
// ResolveTag must resolve through refs/tags only, and ResolveConstraint (which
// KNOWS it selected a tag) must pin the tag's commit.
func TestGitCloneFetcher_ResolveTag_TagWinsOverSameNamedBranch(t *testing.T) {
	tmpDir := t.TempDir()

	// Origin: c1 carries the tags; a branch named exactly like each tag points
	// at the later c2.
	originDir := filepath.Join(tmpDir, "origin")
	origin, err := git.PlainInit(originDir, false)
	require.NoError(t, err)
	originWT, err := origin.Worktree()
	require.NoError(t, err)
	commit := func(content string) plumbing.Hash {
		require.NoError(t, os.WriteFile(filepath.Join(originDir, "file.txt"), []byte(content), 0o644))
		_, aerr := originWT.Add("file.txt")
		require.NoError(t, aerr)
		h, cerr := originWT.Commit(content, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t.com", When: time.Now()},
		})
		require.NoError(t, cerr)
		return h
	}
	c1 := commit("first")
	_, err = origin.CreateTag("v1.0.0", c1, nil) // lightweight
	require.NoError(t, err)
	_, err = origin.CreateTag("v2.0.0", c1, &git.CreateTagOptions{ // annotated
		Message: "v2.0.0",
		Tagger:  &object.Signature{Name: "t", Email: "t@t.com", When: time.Now()},
	})
	require.NoError(t, err)

	c2 := commit("second")
	require.NotEqual(t, c1, c2)
	for _, name := range []string{"v1.0.0", "v2.0.0"} {
		require.NoError(t, origin.Storer.SetReference(
			plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), c2)))
	}

	// Clone: refs/remotes/origin/v1.0.0 (branch, c2) and refs/tags/v1.0.0 (c1)
	// now collide on the bare name.
	cloneDir := filepath.Join(tmpDir, "clone")
	_, err = git.PlainClone(cloneDir, false, &git.CloneOptions{URL: "file://" + originDir})
	require.NoError(t, err)

	fetcher, err := NewGitCloneFetcher(cloneDir, "file://"+originDir, ForgeGitHub, nil)
	require.NoError(t, err)
	ctx := context.Background()

	// Prove the fixture creates a real collision: the generic resolution picks
	// the BRANCH tip (this is the documented ambiguous-ref order, unchanged).
	got, err := fetcher.ResolveRef(ctx, "owner", "repo", "v1.0.0")
	require.NoError(t, err)
	require.Equal(t, c2.String(), got, "fixture sanity: generic ResolveRef must see the colliding branch")

	t.Run("ResolveTag resolves the lightweight tag's commit", func(t *testing.T) {
		sha, terr := fetcher.ResolveTag(ctx, "owner", "repo", "v1.0.0")
		require.NoError(t, terr)
		assert.Equal(t, c1.String(), sha)
	})

	t.Run("ResolveTag dereferences the annotated tag's commit", func(t *testing.T) {
		sha, terr := fetcher.ResolveTag(ctx, "owner", "repo", "v2.0.0")
		require.NoError(t, terr)
		assert.Equal(t, c1.String(), sha)
	})

	t.Run("ResolveTag misses a non-tag", func(t *testing.T) {
		_, terr := fetcher.ResolveTag(ctx, "owner", "repo", "master")
		require.Error(t, terr)
	})

	t.Run("semver constraint resolution pins the tag, not the branch", func(t *testing.T) {
		for _, expr := range []string{"v1.0.0", "v2.0.0"} {
			res, rerr := ResolveConstraint(ctx, expr, NewFetcherRepoVersions(fetcher, "owner", "repo"))
			require.NoError(t, rerr)
			assert.Equal(t, c1.String(), res.SHA, "constraint %q must pin the tag's commit, not the same-named branch tip", expr)
			assert.Equal(t, expr, res.Version)
		}
	})
}
