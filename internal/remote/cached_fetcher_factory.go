package remote

import (
	"context"
	"fmt"
)

// NewCachedFetcherFactory returns a FetcherFactory that uses local git clones
// for all content operations. The remote is cloned on first touch and every
// subsequent FetchFile / ListDir / ResolveRef / GetDefaultBranch read is served
// from the local clone — no per-call forge API traffic.
//
// SearchRepos has no filesystem equivalent (it queries a forge's search index),
// so it is the only operation that still hits the forge API. It does so lazily,
// constructing an API fetcher only when SearchRepos is actually called.
func NewCachedFetcherFactory(cache *RepoCache) FetcherFactory {
	return func(repoURL string, auth AuthConfig) (Fetcher, error) {
		forgeType, _, err := DetectForge(repoURL)
		if err != nil {
			return nil, fmt.Errorf("failed to detect forge for %s: %w", repoURL, err)
		}
		return &cacheFetcher{
			cache:     cache,
			repoURL:   repoURL,
			forgeType: forgeType,
			auth:      auth,
		}, nil
	}
}

// cacheFetcher implements Fetcher by routing every content/ref operation
// through the local clone cache. SearchRepos is the only API path, and it is
// constructed lazily so the API client is never touched for routine work.
type cacheFetcher struct {
	cache     *RepoCache
	repoURL   string
	forgeType ForgeType
	auth      AuthConfig
}

// Forge returns the forge type detected from the repo URL.
func (f *cacheFetcher) Forge() ForgeType {
	return f.forgeType
}

// localFetcher ensures the clone has the requested ref present locally, then
// returns a GitCloneFetcher rooted at the cached repo directory.
func (f *cacheFetcher) localFetcher(ctx context.Context, ref string) (*GitCloneFetcher, error) {
	repoDir, err := f.cache.EnsureRef(ctx, f.repoURL, f.forgeType, ref)
	if err != nil {
		return nil, err
	}
	gitAuth := f.cache.authMethod(f.forgeType)
	return NewGitCloneFetcher(repoDir, normalizeCloneURL(f.repoURL), f.forgeType, gitAuth)
}

func (f *cacheFetcher) FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	gf, err := f.localFetcher(ctx, ref)
	if err != nil {
		return nil, err
	}
	return gf.FetchFile(ctx, owner, repo, path, ref)
}

func (f *cacheFetcher) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	gf, err := f.localFetcher(ctx, ref)
	if err != nil {
		return nil, err
	}
	return gf.ListDir(ctx, owner, repo, path, ref)
}

func (f *cacheFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	gf, err := f.localFetcher(ctx, ref)
	if err != nil {
		return "", err
	}
	return gf.ResolveRef(ctx, owner, repo, ref)
}

func (f *cacheFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	gf, err := f.localFetcher(ctx, "")
	if err != nil {
		return false, err
	}
	return gf.ValidateRepo(ctx, owner, repo)
}

func (f *cacheFetcher) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	gf, err := f.localFetcher(ctx, "")
	if err != nil {
		return "", err
	}
	return gf.GetDefaultBranch(ctx, owner, repo)
}

// SearchRepos has no local equivalent; it queries the forge's search index.
// Constructed lazily so the API client is only created when search is invoked.
func (f *cacheFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	apiFetcher, err := NewFetcher(f.repoURL, f.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create API fetcher for search: %w", err)
	}
	return apiFetcher.SearchRepos(ctx, query, limit)
}
