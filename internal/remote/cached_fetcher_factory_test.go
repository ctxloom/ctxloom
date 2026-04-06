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

	bundleDir := filepath.Join(sourceDir, "ctxloom", "v1", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "test.yaml"), []byte("version: v1\n"), 0644))

	_, err = wt.Add("ctxloom/v1/bundles/test.yaml")
	require.NoError(t, err)

	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Set up cache
	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Track if fallback was called
	fallbackCalled := false
	fallback := func(repoURL string, auth AuthConfig) (Fetcher, error) {
		fallbackCalled = true
		return NewGitHubFetcher(auth.GitHub), nil
	}

	factory := NewCachedFetcherFactory(cache, fallback)

	// Create fetcher using file:// URL
	fetcher, err := factory("file://"+sourceDir, AuthConfig{})
	require.NoError(t, err)
	assert.False(t, fallbackCalled, "should not have called fallback")

	// Should be able to fetch files
	content, err := fetcher.FetchFile(context.Background(), "owner", "repo", "ctxloom/v1/bundles/test.yaml", "")
	require.NoError(t, err)
	assert.Contains(t, string(content), "version: v1")
}

func TestCachedFetcherFactory_FallbackOnCloneFailure(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, AuthConfig{})

	fallbackCalled := false
	fallback := func(repoURL string, auth AuthConfig) (Fetcher, error) {
		fallbackCalled = true
		return NewGitHubFetcher(auth.GitHub), nil
	}

	factory := NewCachedFetcherFactory(cache, fallback)

	// Use a URL that can't be cloned
	fetcher, err := factory("https://github.com/nonexistent/repo-that-does-not-exist-12345", AuthConfig{})
	// Should fall back to API fetcher (which may fail later, but factory succeeds)
	require.NoError(t, err)
	assert.True(t, fallbackCalled, "should have called fallback")
	assert.NotNil(t, fetcher)
}

func TestCachedFetcherWithFallback_SearchReposDelegatesToAPI(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal source repo
	sourceDir := filepath.Join(tmpDir, "source")
	repo, err := git.PlainInit(sourceDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte("test"), 0644))
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	fallbackCalled := false
	fallback := func(repoURL string, auth AuthConfig) (Fetcher, error) {
		fallbackCalled = true
		mock := NewMockFetcher()
		mock.Repos = []RepoInfo{{Name: "test-repo"}}
		return mock, nil
	}

	factory := NewCachedFetcherFactory(cache, fallback)
	fetcher, err := factory("file://"+sourceDir, AuthConfig{})
	require.NoError(t, err)

	// SearchRepos should delegate to fallback
	repos, err := fetcher.SearchRepos(context.Background(), "test", 10)
	require.NoError(t, err)
	assert.True(t, fallbackCalled)
	assert.Len(t, repos, 1)
	assert.Equal(t, "test-repo", repos[0].Name)
}
