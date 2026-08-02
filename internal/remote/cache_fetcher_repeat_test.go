package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every FetchFile / ListDir / ResolveRef / ListTags
// call re-runs localFetcher, which re-takes the per-directory clone lock and
// re-runs git.PlainOpen on a repo that is already there. Accurate, and
// measured on this machine at roughly 0.6 ms per call against a ~1.5 ms read
// -- about a quarter of the cached read path.
//
// The re-open is NOT removed; the reasoning is in the commit. What was missing
// either way is any pin that repeated calls through this decorator agree with
// each other, which is the property a caching or memoizing change would have
// to preserve. These are characterization tests: green before such a change
// and green after, so the next person to take the re-open out has something
// that fails if the answers start drifting.
func TestCacheFetcher_RepeatedCallsAgree(t *testing.T) {
	ctx := context.Background()
	sourceRepo, _ := createTestRepoWithFiles(t, t.TempDir())
	repoURL := "file://" + sourceRepo

	cache := NewRepoCache(t.TempDir(), AuthConfig{})
	fetcher, err := NewCachedFetcherFactory(cache)(repoURL, AuthConfig{})
	require.NoError(t, err)

	const bundlePath = ".ctxloom/content/bundles/core.yaml"

	t.Run("FetchFile", func(t *testing.T) {
		first, err := fetcher.FetchFile(ctx, "o", "r", bundlePath, "")
		require.NoError(t, err)
		assert.NotEmpty(t, first)
		for i := 0; i < 3; i++ {
			again, err := fetcher.FetchFile(ctx, "o", "r", bundlePath, "")
			require.NoError(t, err)
			assert.Equal(t, first, again, "read %d disagreed with the first", i)
		}
	})

	t.Run("ListDir", func(t *testing.T) {
		first, err := fetcher.ListDir(ctx, "o", "r", ".ctxloom/content/bundles", "")
		require.NoError(t, err)
		require.NotEmpty(t, first)
		again, err := fetcher.ListDir(ctx, "o", "r", ".ctxloom/content/bundles", "")
		require.NoError(t, err)
		assert.Equal(t, first, again)
	})

	t.Run("GetDefaultBranch and ResolveRef", func(t *testing.T) {
		branch, err := fetcher.GetDefaultBranch(ctx, "o", "r")
		require.NoError(t, err)
		assert.NotEmpty(t, branch)

		sha, err := fetcher.ResolveRef(ctx, "o", "r", branch)
		require.NoError(t, err)
		againSHA, err := fetcher.ResolveRef(ctx, "o", "r", branch)
		require.NoError(t, err)
		assert.Equal(t, sha, againSHA)
	})

	t.Run("ValidateRepo", func(t *testing.T) {
		ok, err := fetcher.ValidateRepo(ctx, "o", "r")
		require.NoError(t, err)
		assert.True(t, ok)
		again, err := fetcher.ValidateRepo(ctx, "o", "r")
		require.NoError(t, err)
		assert.True(t, again)
	})

	t.Run("ListTags", func(t *testing.T) {
		tagger, ok := fetcher.(tagLister)
		require.True(t, ok)
		first, err := tagger.ListTags(ctx, "o", "r")
		require.NoError(t, err)
		again, err := tagger.ListTags(ctx, "o", "r")
		require.NoError(t, err)
		assert.Equal(t, first, again)
	})

	// The property the re-open currently buys, and the one a memo would have to
	// give up: a clone that disappears underneath the fetcher is re-made on the
	// next call rather than turning every later read into an error.
	t.Run("a clone removed mid-life is restored on the next call", func(t *testing.T) {
		repoDir, err := cache.RepoDirForURL(repoURL)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(filepath.Join(repoDir, ".git")))

		got, err := fetcher.FetchFile(ctx, "o", "r", bundlePath, "")
		require.NoError(t, err, "the re-open is what makes this self-healing")
		assert.NotEmpty(t, got)
	})
}
