package operations

import (
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// shortSHALen is the number of hex characters in an abbreviated commit SHA.
const shortSHALen = 7

// shortSHA abbreviates a commit SHA to shortSHALen characters, returning it
// unchanged when shorter. Guards against panics on malformed/short SHAs read
// from a lockfile.
func shortSHA(sha string) string {
	if len(sha) > shortSHALen {
		return sha[:shortSHALen]
	}
	return sha
}

// getFS returns the provided filesystem or a default OS filesystem if nil.
func getFS(fs afero.Fs) afero.Fs {
	if fs == nil {
		return afero.NewOsFs()
	}
	return fs
}

// loadFreshConfig loads a fresh config, or returns testConfig if provided.
// This pattern is used by operations that modify config and need a fresh copy.
func loadFreshConfig(fs afero.Fs, appDir string, testConfig *config.Config) (*config.Config, error) {
	if testConfig != nil {
		return testConfig, nil
	}

	fs = getFS(fs)
	opts := []config.LoadOption{config.WithFS(fs)}
	if appDir != "" {
		opts = append(opts, config.WithAppDir(appDir))
	}

	cfg, err := config.Load(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}

// newRepoCache creates a RepoCache rooted at the standard cache path for cfg.
// The cache carries a forge resolver derived from the remotes registry so the
// github clone token-injection reads the per-forge token_env; resolver setup is
// best-effort and a missing/unreadable registry simply falls back to ambient
// auth.
func newRepoCache(cfg *config.Config) *remote.RepoCache {
	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)

	var opts []remote.RepoCacheOption
	if registry, err := remote.NewRegistry(paths.RemotesPath(baseDir)); err == nil {
		forges := registry.Forges()
		opts = append(opts, remote.WithForgeResolver(func(repoURL string) remote.ResolvedForge {
			return remote.ResolveForgeForURLWith(repoURL, "", forges)
		}))
	}
	return remote.NewRepoCache(paths.ReposCachePath(baseDir), auth, opts...)
}

// newCachedFetcherFactory returns a FetcherFactory backed by the local clone
// cache. All content/ref operations are local; SearchRepos is the only path
// that may hit the forge API.
func newCachedFetcherFactory(cfg *config.Config) remote.FetcherFactory {
	return remote.NewCachedFetcherFactory(newRepoCache(cfg))
}

// getCachedFetcher returns a Fetcher for repoURL using the local clone cache.
// Used by operations that need to read a single remote (browse, pull,
// fetch-content); for bulk operations across many entries prefer newRepoCache
// + UpdateRepo to dedup work per URL.
func getCachedFetcher(cfg *config.Config, repoURL string) (remote.Fetcher, error) {
	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)
	return newCachedFetcherFactory(cfg)(repoURL, auth)
}

// NewRepoCache returns a RepoCache rooted at the standard cache path for cfg.
// Exported so cmd callers (e.g. `remote update`) can pre-fetch per-URL before
// looping over many lockfile entries.
func NewRepoCache(cfg *config.Config) *remote.RepoCache {
	return newRepoCache(cfg)
}

// NewCachedFetcherFactory returns a FetcherFactory backed by the local clone
// cache, exported for cmd callers that build their own Puller / iterate
// remotes directly.
func NewCachedFetcherFactory(cfg *config.Config) remote.FetcherFactory {
	return newCachedFetcherFactory(cfg)
}

// GetCachedFetcher returns a Fetcher for repoURL using the local clone cache.
// Exported version of getCachedFetcher for cmd callers.
func GetCachedFetcher(cfg *config.Config, repoURL string) (remote.Fetcher, error) {
	return getCachedFetcher(cfg, repoURL)
}
