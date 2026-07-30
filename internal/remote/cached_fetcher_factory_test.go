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

func TestCachedFetcherFactory_UsesCache(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source repo
	sourceDir := filepath.Join(tmpDir, "source")
	repo, err := git.PlainInit(sourceDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(sourceDir, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), []byte("version: v1\n"), 0644))

	_, err = wt.Add(".ctxloom/content/bundles/test.yaml")
	require.NoError(t, err)

	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Set up cache
	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	factory := NewCachedFetcherFactory(cache)

	// Create fetcher using file:// URL
	fetcher, err := factory("file://"+sourceDir, AuthConfig{})
	require.NoError(t, err)

	// Should be able to fetch files
	content, err := fetcher.FetchFile(context.Background(), "owner", "repo", ".ctxloom/content/bundles/test.yaml", "")
	require.NoError(t, err)
	assert.Contains(t, string(content), "version: v1")
}

func TestCachedFetcherFactory_ContentFailsHardOnCloneFailure(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, AuthConfig{})

	factory := NewCachedFetcherFactory(cache)

	// Factory itself doesn't clone — it just constructs the wrapper.
	fetcher, err := factory("https://example.invalid/nonexistent/repo", AuthConfig{})
	require.NoError(t, err)
	require.NotNil(t, fetcher)

	// Content ops should fail hard once they try to clone the bogus URL,
	// not silently fall back to an API fetcher.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = fetcher.FetchFile(ctx, "owner", "repo", "any/file.yaml", "")
	require.Error(t, err)
}

func TestCachedFetcher_SearchReposUsesAPI(t *testing.T) {
	// SearchRepos has no filesystem equivalent, so the cached fetcher must
	// lazily construct an API fetcher to serve it. The API call itself isn't
	// what's being tested here — we only verify the cached fetcher routes to
	// the API path (and therefore doesn't try to use the local clone).
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	factory := NewCachedFetcherFactory(cache)
	fetcher, err := factory("https://github.com/owner/repo", AuthConfig{})
	require.NoError(t, err)

	cf, ok := fetcher.(*cacheFetcher)
	require.True(t, ok, "expected cacheFetcher")
	assert.Equal(t, ForgeGitHub, cf.Forge())
}

// TestCachedFetcher_SearchReposUnsupportedForGenericGit pins that "this forge
// has no search index" is not delivered as "your query matched nothing".
//
// U093-F18: the generic git adapter returned (nil, nil), which any caller reads
// as an empty result set. There is no filesystem or clone equivalent of a
// forge-wide search — the adapter is handed one known URL and cannot enumerate
// a host — so the honest answer is an error, not silence.
func TestCachedFetcher_SearchReposUnsupportedForGenericGit(t *testing.T) {
	cache := NewRepoCache(filepath.Join(t.TempDir(), "cache"), AuthConfig{})
	fetcher, err := NewCachedFetcherFactory(cache)("https://git.company.example/owner/repo", AuthConfig{})
	require.NoError(t, err)
	require.Equal(t, ForgeGitGeneric, fetcher.Forge())

	repos, err := fetcher.SearchRepos(context.Background(), "anything", 10)
	require.Error(t, err, "unsupported must not be reported as zero results")
	assert.Empty(t, repos)
	assert.ErrorIs(t, err, ErrSearchUnsupported)
}

func TestCachedFetcher_EnsureRefUnshallowsForTags(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a source repo with two commits and a tag on the older commit.
	sourceDir := filepath.Join(tmpDir, "source")
	repo, err := git.PlainInit(sourceDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(sourceDir, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), []byte("v1\n"), 0644))
	_, err = wt.Add(".ctxloom/content/bundles/test.yaml")
	require.NoError(t, err)
	taggedHash, err := wt.Commit("first", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)

	// Create a lightweight tag at the first commit.
	_, err = repo.CreateTag("v0.1.0", taggedHash, nil)
	require.NoError(t, err)

	// Second commit so HEAD moves past the tag.
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), []byte("v2\n"), 0644))
	_, err = wt.Add(".ctxloom/content/bundles/test.yaml")
	require.NoError(t, err)
	_, err = wt.Commit("second", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	factory := NewCachedFetcherFactory(cache)
	fetcher, err := factory("file://"+sourceDir, AuthConfig{})
	require.NoError(t, err)

	// Pulling the tag triggers unshallow; the older commit becomes locally
	// resolvable and we get back the v1 content.
	content, err := fetcher.FetchFile(context.Background(), "owner", "repo", ".ctxloom/content/bundles/test.yaml", "v0.1.0")
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(content))
}
