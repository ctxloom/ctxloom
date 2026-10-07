package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// checkoutOf is the bundle checkout (git worktree) directory ref is installed
// into.
func (p *movedPinProject) checkoutOf(t *testing.T, ref string) string {
	t.Helper()
	parsed, err := remote.ParseReference(ref)
	require.NoError(t, err)
	dir, err := parsed.LocalWorktreePath(p.appDir)
	require.NoError(t, err)
	return dir
}

func (p *movedPinProject) pullOnce(t *testing.T) *SyncDependenciesResult {
	t.Helper()
	p.cfg(t)
	res, err := SyncDependencies(context.Background(), p.app, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)
	return res
}

// A successful pull deletes the checkout of a bundle the lock no longer names,
// says which, and leaves every checkout the lock still names.
func TestPull_PrunesTheCheckoutOfABundleTheLockDropped(t *testing.T) {
	bothCompositions(t, func(t *testing.T, compose composeFinder) {
		p := newMovedPinProject(t, compose)
		tool := p.checkoutOf(t, p.toolRef)
		require.DirExists(t, tool, "the dropped bundle's checkout is still installed before the pull")

		res := p.pullOnce(t)

		require.Empty(t, res.Failed)
		assert.NoDirExists(t, tool, "the checkout the lock no longer names is deleted")
		assert.Equal(t, []string{tool}, res.PrunedCheckouts, "the pull names the checkout it deleted")
		assert.DirExists(t, p.checkoutOf(t, p.rolesRef))
		assert.DirExists(t, p.checkoutOf(t, p.keepRef))

		again := p.pullOnce(t)
		assert.Empty(t, again.PrunedCheckouts, "nothing is left to prune")
	})
}

// SHARED ROOT: a checkout root that is not physically this project's own (a
// symlinked cache, which another project could share) holds checkouts other
// lockfiles may name. Pull cannot read those lockfiles, so it prunes nothing.
func TestPull_KeepsCheckoutsWhenTheCheckoutRootIsShared(t *testing.T) {
	p := newMovedPinProject(t, viaLocalParent)
	tool := p.checkoutOf(t, p.toolRef)
	root := paths.CacheBundlesPath(p.appDir)
	shared := filepath.Join(t.TempDir(), "shared-bundles")
	require.NoError(t, os.Rename(root, shared))
	require.NoError(t, os.Symlink(shared, root))

	res := p.pullOnce(t)

	require.Empty(t, res.Failed)
	assert.DirExists(t, tool, "a checkout in a shared root is never pruned")
	assert.Empty(t, res.PrunedCheckouts)
}

// A checkout registered to a clone outside this project's clone cache belongs
// to whoever owns that clone; it is not pruned.
func TestPull_KeepsACheckoutRegisteredToAnotherClone(t *testing.T) {
	p := newMovedPinProject(t, viaLocalParent)
	tool := p.checkoutOf(t, p.toolRef)
	foreign := filepath.Join(t.TempDir(), "other-clone", ".git", "worktrees", "tool")
	require.NoError(t, os.MkdirAll(foreign, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tool, ".git"), []byte("gitdir: "+foreign+"\n"), 0o644))

	res := p.pullOnce(t)

	require.Empty(t, res.Failed)
	assert.DirExists(t, tool, "a checkout another clone owns is never pruned")
	assert.Empty(t, res.PrunedCheckouts)
}

// A pull that failed proves nothing about the lock: it prunes nothing.
func TestPull_DoesNotPruneAfterAFailedPull(t *testing.T) {
	p := newMovedPinProject(t, viaLocalParent)
	tool := p.checkoutOf(t, p.toolRef)
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, p.appDir), "dev.yaml"),
		[]byte("parents:\n  - "+p.rolesRef+"#profiles/finder\nbundles:\n  - "+p.repoURL+"@bundles/missing\n"), 0o644))

	res := p.pullOnce(t)

	require.NotEmpty(t, res.Failed, "the pull fails")
	assert.DirExists(t, tool, "nothing is pruned after a failed pull")
	assert.Empty(t, res.PrunedCheckouts)
}

// CONTAINMENT: a symlink inside the checkout root is never followed, so a
// directory outside the root survives even when it looks exactly like a
// checkout of this project's clone.
func TestPull_PruneNeverFollowsASymlinkOutOfTheCheckoutRoot(t *testing.T) {
	p := newMovedPinProject(t, viaLocalParent)
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	clones := paths.ReposCachePath(p.appDir)
	require.NoError(t, os.WriteFile(filepath.Join(outside, ".git"), []byte("gitdir: "+clones+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "precious"), []byte("keep me\n"), 0o644))
	link := filepath.Join(paths.CacheBundlesPath(p.appDir), "evil"+remote.WorktreeDirSuffix)
	require.NoError(t, os.Symlink(outside, link))

	res := p.pullOnce(t)

	require.Empty(t, res.Failed)
	assert.FileExists(t, filepath.Join(outside, "precious"), "nothing outside the root is deleted")
	_, err := os.Lstat(link)
	assert.NoError(t, err, "the symlink itself is not touched")
	assert.NotContains(t, res.PrunedCheckouts, link)
	assert.Equal(t, []string{p.checkoutOf(t, p.toolRef)}, res.PrunedCheckouts, "the real dropped checkout is still pruned")
}
