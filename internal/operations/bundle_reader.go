package operations

import (
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// NewBundleReaderForConfig wires a cached BundleByteSource from cfg using
// the standard cached fetcher factory and the active lockfile on disk.
// Returns nil if the lockfile can't be loaded.
//
// It serves callers that need reader access independent of the bundles.Loader
// seeding pipeline cfg.BundleLoader encapsulates — the installed-probe path
// below being the one that must never be given a source it cannot read with.
//
// A nil return is NOT a neutral value downstream. isInstalled treats a nil
// source as "nothing is installed", so an unreadable lockfile makes every
// remote bundle in the project report as missing — the sync probe then plans
// to re-install a fully-installed project, and the user is told a pile of
// dependencies is absent. That verdict is defensible (an unreadable lockfile
// really does mean nothing can be proven installed) but it must never be
// reached in silence, hence the warning: the caller degrades, the user is
// told which file caused it.
//
// remote.BundleReader takes a *remote.Registry parameter but never reads it,
// so this deliberately passes nil rather than loading one from disk only to
// discard the possibility of failure it can't actually produce.
func NewBundleReaderForConfig(cfg *config.Config) remote.BundleByteSource {
	baseDir := getBaseDir(cfg)
	lock, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		clidiag.Warn("ctxloom",
			"cannot read %s: %v — every remote bundle will be reported as not installed until this is fixed",
			paths.LockPath(baseDir), err)
		return nil
	}
	return remote.NewCachingBundleReader(
		remote.NewBundleReader(nil, NewCachedFetcherFactory(cfg), remote.LoadAuth(baseDir), lock,
			// A DIRECTORY-form bundle must be readable HERE, because isInstalled
			// decides installedness by reading the bundle's bytes. Without this
			// the reader refuses every tree entry, so a fully pulled skill
			// bundle reports as missing on every probe and is re-pulled on every
			// sync, forever — the read is the probe, so a form it cannot read is
			// a form it can never call installed.
			//
			// The walker lives in the content layer, above remote, so it is
			// composed in here for the same reason NewPuller composes it (see
			// remote.TreeFetchFunc). The cached fetcher factory keeps the walk on
			// the local clone.
			remote.WithReaderTreeFetcher(remotetree.PullTreeFetcher),
		),
	)
}
