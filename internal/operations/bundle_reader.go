package operations

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// NewBundleReaderForConfig wires a cached BundleByteSource from cfg using
// the standard cached fetcher factory and the active lockfile on disk.
// Returns nil if the registry or lockfile can't be loaded.
//
// Used by the review-flow tools (show_bundle_verbatim, etc.) that need
// reader access independent of the bundles.Loader seeding pipeline that
// cfg.SeededBundleLoader already encapsulates.
func NewBundleReaderForConfig(cfg *config.Config) remote.BundleByteSource {
	baseDir := getBaseDir(cfg)
	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir))
	if err != nil {
		return nil
	}
	lock, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		return nil
	}
	return remote.NewCachingBundleReader(
		remote.NewBundleReader(registry, NewCachedFetcherFactory(cfg), remote.LoadAuth(baseDir), lock),
	)
}

// NewBundleReaderForLockfile wires a reader against a specific lockfile
// rather than the active one on disk. Used by show_bundle_verbatim to
// preview content from the *pending* lockfile during a review. The cache
// is per-instance — show_bundle_verbatim constructs a fresh reader per
// call, so the cache mostly benefits repeat S<name> requests on the same
// bundle within one MCP call.
func NewBundleReaderForLockfile(cfg *config.Config, lock *remote.Lockfile) remote.BundleByteSource {
	baseDir := getBaseDir(cfg)
	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir))
	if err != nil {
		return nil
	}
	return remote.NewCachingBundleReader(
		remote.NewBundleReader(registry, NewCachedFetcherFactory(cfg), remote.LoadAuth(baseDir), lock),
	)
}
