package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// The lockfile path must come from paths.LockPath, the package that owns this
// layout, not from a private const assembled in internal/remote — a second
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

// Reference.LocalPath must root at paths.CacheBundlesPath rather than
// re-assemble the cache bundles root from paths.CacheDir + paths.BundlesDir,
// so a layout change in internal/paths cannot silently miss it. Pins the two
// to one answer.
//
// It deliberately does NOT route through a layout-specific prefix
// (CacheBundlesPathFor): the cache install side distinguishes a document from
// a tree by EXTENSION (LocalTreePath is LocalPath minus ".yaml"), not by a
// repo-format segment — that segment names where a PUBLISHER commits, and the
// two axes were bound together once and reverted (see "Revert 'point fetch
// and the cache at v2' — it starves every session of context"), because
// rooting the cache in the repo layout orphaned every already-installed
// bundle from the previous format root.
func TestReferenceLocalPath_RootedAtCacheBundlesPath(t *testing.T) {
	r := &Reference{URL: "https://github.com/acme/repo", Path: "lang/go"}
	got := r.LocalPath("/proj/.ctxloom", ItemTypeBundle)
	assert.True(t, len(got) > 0)
	assert.Equal(t,
		paths.CacheBundlesPath("/proj/.ctxloom")+"/"+r.LocalRemoteName()+"/lang/go.yaml",
		got)
}
