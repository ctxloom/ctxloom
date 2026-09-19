package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestInvalidateBundleLoader_NewlyPulledBundleResolvesInTheSameRun pins the
// SINGLE-RUN case of `deps pull`: one Config, the shared loader already built
// (the reference collector builds it before any fetch), then a pull lands a
// lockfile entry and its installed tree, then InvalidateBundleLoader — and the
// post-pull steps that resolve the new ref through that same loader must find
// it.
//
// The two-run case (a fresh process, a fresh reader set) always passed and is
// exactly what hid this: the loader used to re-resolve its ORIGINAL readers on
// invalidate, and the reader for a lockfile entry that did not exist when the
// loader was built was never among them. So the first pull reported the bundle
// it had just installed as not found, withheld its hooks and commands, and
// running pull again was the only fix.
func TestInvalidateBundleLoader_NewlyPulledBundleResolvesInTheSameRun(t *testing.T) {
	testsupport.Isolate(t)
	c, _, _, fsys := stageInstalledTree(t)
	c.DisableCompanionProbe()

	// Before the pull: no lockfile, so the shared loader is built with no
	// remote readers at all. This is the loader the run's post-steps will use.
	_, err := c.BundleLoader().Read(treeCanonical)
	require.Error(t, err, "nothing is pinned yet")

	// The pull lands: the lockfile entry is written, and the tree is already
	// installed by stageInstalledTree. Then the sync announces the change.
	lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{treeCanonical: treeEntry()}}
	require.NoError(t, remote.NewLockfileManager(treeBase, remote.WithLockfileFS(fsys)).Save(lock))
	c.InvalidateBundleLoader()

	read, err := c.BundleLoader().Read(treeCanonical)
	require.NoError(t, err, "the bundle this run just pinned must resolve in this run, not the next one")
	assert.Equal(t, treeCanonical, read.DisplayName())
}
