package operations

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// NewBundleReaderForConfig wires a cached BundleByteSource from cfg using
// the standard cached fetcher factory and the active lockfile on disk.
// Returns nil if the lockfile can't be loaded.
//
// Used by the review-flow tools (show_bundle_verbatim, etc.) that need
// reader access independent of the bundles.Loader seeding pipeline that
// cfg.SeededBundleLoader already encapsulates.
//
// remote.BundleReader takes a *remote.Registry parameter but never reads it
// (U093-F05), so this deliberately passes nil rather than loading one from
// disk only to discard the possibility of failure it can't actually produce.
func NewBundleReaderForConfig(cfg *config.Config) remote.BundleByteSource {
	baseDir := getBaseDir(cfg)
	lock, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		return nil
	}
	return remote.NewCachingBundleReader(
		remote.NewBundleReader(nil, NewCachedFetcherFactory(cfg), remote.LoadAuth(baseDir), lock),
	)
}
