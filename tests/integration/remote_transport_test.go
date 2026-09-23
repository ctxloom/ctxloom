//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoteTransport_CloneFetchResolve exercises the real git-transport surface
// end to end against a local bare repo served over file:// (see ADR 0015):
// RepoCache.EnsureRepo clones it, GitCloneFetcher reads files / resolves refs
// from the clone, and RepoCache.UpdateRepo fetches a later commit. This is the
// genuine clone/fetch/cache code path (go-git treats file:// like https).
func TestRemoteTransport_CloneFetchResolve(t *testing.T) {
	ctx := context.Background()
	repo := testenv.SeedGitRepo(t, testenv.CtxloomContentLayout(t))

	cacheBase := t.TempDir()
	cache := remote.NewRepoCache(cacheBase, remote.AuthConfig{})

	// Clone into the cache.
	localPath, err := cache.EnsureRepo(ctx, repo.URL, remote.ForgeGitHub)
	require.NoError(t, err, "EnsureRepo should clone the file:// remote")

	// The cached clone must stay inside the cache base (repoDirForURL containment).
	rel, err := filepath.Rel(cacheBase, localPath)
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(rel, ".."), "clone %q escaped cache base %q", localPath, cacheBase)

	// Read a seeded file and resolve a ref through the local clone.
	fetcher, err := remote.NewGitCloneFetcher(localPath, repo.URL, remote.ForgeGitHub, nil)
	require.NoError(t, err)

	fragment := testenv.TreeBundleItemPath("demo", "fragments/demo-frag.md")
	content, err := fetcher.FetchFile(ctx, "owner", "repo", fragment, "main")
	require.NoError(t, err)
	assert.Contains(t, string(content), "Demo fragment content.", "should read the seeded bundle from the clone")

	sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "main")
	require.NoError(t, err)
	assert.Equal(t, repo.SHA, sha, "ResolveRef(main) should return the seeded tip SHA")

	// A missing path surfaces the not-found sentinel.
	_, err = fetcher.FetchFile(ctx, "owner", "repo", testenv.TreeBundleManifestPath("missing"), "main")
	require.Error(t, err)

	// Commit a new revision upstream, then UpdateRepo must fetch it.
	// (CommitFile updates repo.SHA, so capture the old tip first.)
	oldSHA := repo.SHA
	newSHA := repo.CommitFile(t, fragment, "Demo fragment content, revision two.\n")
	require.NotEqual(t, oldSHA, newSHA)

	updatedPath, err := cache.UpdateRepo(ctx, repo.URL, remote.ForgeGitHub)
	require.NoError(t, err, "UpdateRepo should fetch the new commit")

	updatedFetcher, err := remote.NewGitCloneFetcher(updatedPath, repo.URL, remote.ForgeGitHub, nil)
	require.NoError(t, err)
	resolved, err := updatedFetcher.ResolveRef(ctx, "owner", "repo", "main")
	require.NoError(t, err)
	assert.Equal(t, newSHA, resolved, "after UpdateRepo, main should resolve to the new SHA")

	// Read by the resolved SHA — this is how production reads (the lockfile pins
	// a SHA, not a branch name), and it proves the new commit's objects were
	// fetched into the cache rather than just the remote-tracking ref moving.
	updatedContent, err := updatedFetcher.FetchFile(ctx, "owner", "repo", fragment, newSHA)
	require.NoError(t, err)
	assert.Contains(t, string(updatedContent), "revision two", "fetch should see the updated content")
}
