package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U093-F17 is CHARACTERIZED here, not fixed: removing the parameters is an
// interface change across two packages, and the escalation is recorded in the
// findings index.
//
// The Fetcher interface threads `owner, repo` through six of its seven
// methods. GitHubFetcher needs them — it is not bound to a repository and puts
// them straight into the API path. GitCloneFetcher and cacheFetcher are each
// constructed FOR one repository and ignore them entirely: the clone directory
// and the repo URL were fixed at construction, and no argument can move the
// read off them.
//
// These tests demonstrate that directly, by asking a clone-backed fetcher for
// a file while naming a completely different repository. They describe today's
// behaviour so the shape of the parameter is visible; they do not endorse it.
func TestGitCloneFetcher_OwnerAndRepoAreInert_Characterization(t *testing.T) {
	ctx := context.Background()
	repoDir, sha := createTestRepoWithFiles(t, t.TempDir())

	fetcher, err := NewGitCloneFetcher(repoDir, "file://"+repoDir, ForgeGitHub, nil)
	require.NoError(t, err)

	const path = ".ctxloom/content/bundles/core.yaml"

	t.Run("a read names one repository and gets another", func(t *testing.T) {
		fromRightRepo, err := fetcher.FetchFile(ctx, "alice", "ctxloom", path, sha)
		require.NoError(t, err)

		fromWrongRepo, err := fetcher.FetchFile(ctx, "mallory", "not-the-same-repo-at-all", path, sha)
		require.NoError(t, err, "naming a different repository is not even an error")
		assert.Equal(t, fromRightRepo, fromWrongRepo,
			"the arguments do not scope the read; the clone directory does")
	})

	t.Run("empty owner and repo read exactly the same bytes", func(t *testing.T) {
		// Which is what treeAtRef already relies on: it calls GetDefaultBranch
		// with ("", "") internally, so any scoping check added to these
		// parameters would have to exempt its own caller.
		named, err := fetcher.FetchFile(ctx, "alice", "ctxloom", path, sha)
		require.NoError(t, err)
		blank, err := fetcher.FetchFile(ctx, "", "", path, sha)
		require.NoError(t, err)
		assert.Equal(t, named, blank)
	})

	t.Run("every production caller derives them from the fetcher's OWN url", func(t *testing.T) {
		// The consequence half of the row -- "a caller that believes it scoped
		// a read to a repository did not" -- has no live instance, and this is
		// why. BundleReader.fetchAtLockedSHA and FetchRefBytes both build the
		// fetcher from a URL and then parse that SAME URL for owner/repo, so
		// the arguments cannot disagree with the binding.
		owner, repo, err := ParseOwnerRepo("https://github.com/alice/ctxloom")
		require.NoError(t, err)
		assert.Equal(t, "alice", owner)
		assert.Equal(t, "ctxloom", repo)
	})
}
