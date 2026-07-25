package operations

import (
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

// NewRepoCache creates a RepoCache rooted at the standard cache path for cfg.
// Exported so cmd callers (e.g. `remote update`) can pre-fetch per-URL before
// looping over many lockfile entries.
// The cache carries a forge resolver derived from the remotes registry so the
// github clone token-injection reads the per-forge token_env; resolver setup is
// best-effort and a missing/unreadable registry simply falls back to ambient
// auth.
func NewRepoCache(cfg *config.Config) *remote.RepoCache {
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

// NewCachedFetcherFactory returns a FetcherFactory backed by the local clone
// cache, also used by cmd callers that build their own Puller or iterate
// remotes directly. All content/ref operations are local; SearchRepos is the only path
// that may hit the forge API.
func NewCachedFetcherFactory(cfg *config.Config) remote.FetcherFactory {
	return remote.NewCachedFetcherFactory(NewRepoCache(cfg))
}

// GetCachedFetcher returns a Fetcher for repoURL using the local clone cache.
// Used by operations that need to read a single remote (browse, pull,
// fetch-content); for bulk operations across many entries prefer NewRepoCache
// + UpdateRepo to dedup work per URL.
func GetCachedFetcher(cfg *config.Config, repoURL string) (remote.Fetcher, error) {
	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)
	return NewCachedFetcherFactory(cfg)(repoURL, auth)
}
