package remote

import (
	"context"
	"fmt"
	"os"
)

// NewCachedFetcherFactory returns a FetcherFactory that uses local git clones
// for file operations, falling back to API-based fetchers on failure.
// SearchRepos always delegates to the API since it requires forge-level search.
func NewCachedFetcherFactory(cache *RepoCache, fallback FetcherFactory) FetcherFactory {
	return func(repoURL string, auth AuthConfig) (Fetcher, error) {
		forgeType, _, err := DetectForge(repoURL)
		if err != nil {
			return fallback(repoURL, auth)
		}

		// Ensure the repo is cloned/up-to-date
		repoDir, err := cache.EnsureRepo(context.Background(), repoURL, forgeType)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: git cache failed for %s, using API: %v\n", repoURL, err)
			return fallback(repoURL, auth)
		}

		// Create the local fetcher
		gitAuth := cache.authMethod(forgeType)
		localFetcher, err := NewGitCloneFetcher(repoDir, normalizeCloneURL(repoURL), forgeType, gitAuth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: failed to open cached repo, using API: %v\n", err)
			return fallback(repoURL, auth)
		}

		// Wrap to delegate SearchRepos to API
		return &cachedFetcherWithFallback{
			primary:  localFetcher,
			fallback: fallback,
			repoURL:  repoURL,
			auth:     auth,
		}, nil
	}
}

// cachedFetcherWithFallback wraps a GitCloneFetcher and delegates
// SearchRepos to the API-based fallback fetcher.
type cachedFetcherWithFallback struct {
	primary  *GitCloneFetcher
	fallback FetcherFactory
	repoURL  string
	auth     AuthConfig
}

func (f *cachedFetcherWithFallback) Forge() ForgeType {
	return f.primary.Forge()
}

func (f *cachedFetcherWithFallback) FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	return f.primary.FetchFile(ctx, owner, repo, path, ref)
}

func (f *cachedFetcherWithFallback) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	return f.primary.ListDir(ctx, owner, repo, path, ref)
}

func (f *cachedFetcherWithFallback) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	return f.primary.ResolveRef(ctx, owner, repo, ref)
}

func (f *cachedFetcherWithFallback) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	return f.primary.ValidateRepo(ctx, owner, repo)
}

func (f *cachedFetcherWithFallback) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	return f.primary.GetDefaultBranch(ctx, owner, repo)
}

func (f *cachedFetcherWithFallback) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	apiFetcher, err := f.fallback(f.repoURL, f.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create API fetcher for search: %w", err)
	}
	return apiFetcher.SearchRepos(ctx, query, limit)
}
