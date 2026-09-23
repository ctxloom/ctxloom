package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The lockfile path must come from paths.LockPath, the package that owns this
// layout, not from a private const assembled in internal/adapters/remote — a second
// construction leaves paths.LockPath with zero production callers and free to
// drift. This pins the two to one answer, including the default-baseDir branch.
func TestLockfileManagerPath_MatchesPathsLockPath(t *testing.T) {
	for _, base := range []string{"/proj/.ctxloom", ".ctxloom", "/tmp/x/.ctxloom"} {
		assert.Equal(t, paths.LockPath(base), NewLockfileManager(base).Path(),
			"lockfile path must come from paths.LockPath, not a private copy")
	}
	// An empty baseDir defaults to the bare .ctxloom dir name.
	assert.Equal(t, paths.LockPath(paths.AppDirName), NewLockfileManager("").Path())
}

// Reference.LocalWorktreePath must root at paths.CacheBundlesPath rather than
// re-assemble the cache bundles root from paths.CacheDir + paths.BundlesDir,
// so a layout change in internal/core/paths cannot silently miss it. Pins the two
// to one answer.
//
// It deliberately does NOT route through a layout-specific prefix
// (CacheBundlesPathFor): the repo-format segment names where a PUBLISHER
// commits, and rooting the cache in the repo layout was tried and reverted
// ("Revert 'point fetch and the cache at v2' — it starves every session of
// context") because it orphaned every already-installed bundle from the
// previous format root.
func TestReferenceLocalWorktreePath_RootedAtCacheBundlesPath(t *testing.T) {
	r := &Reference{URL: "https://github.com/acme/repo", Path: "lang/go"}
	assert.Equal(t,
		paths.CacheBundlesPath("/proj/.ctxloom")+"/"+r.LocalRemoteName()+"/lang/go"+WorktreeDirSuffix,
		r.LocalWorktreePath("/proj/.ctxloom"))
}
