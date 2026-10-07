package remote

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/trust"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// The directory-form pull path. What these tests pin is not "a tree can be
// fetched" — the acceptance journey proves that end to end — but the three
// decisions that are invisible from outside and would each fail silently:
// which shape is probed first, which errors are allowed to trigger the probe,
// and whether the install is a replace or a merge.

const treeTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// treePuller builds a Puller whose lockfile (and therefore whose tree install
// root) lives on an in-memory fs under baseDir.
func treePuller(t *testing.T, fs afero.Fs, baseDir string, tf TreeFetchFunc) *Puller {
	t.Helper()
	opts := []PullerOption{
		WithLockfileManager(NewLockfileManager(baseDir, WithLockfileFS(fs))),
	}
	if tf != nil {
		opts = append(opts, WithTreeFetcher(tf))
	}
	return NewPuller(nil, AuthConfig{}, opts...)
}

func treeRef(t *testing.T) *Reference {
	t.Helper()
	ref, err := ParseReference("https://github.com/trent/atelier@bundles/atelier")
	require.NoError(t, err)
	return ref
}

// TestFetchItemBytes_ReadsTheTreeAndTakesItsManifestAsTheBundleBytes.
func TestFetchItemBytes_ReadsTheTreeAndTakesItsManifestAsTheBundleBytes(t *testing.T) {
	want := map[string]TreeFile{
		BundleManifestName:               {Data: []byte("version: \"2.0.0\"\n")},
		"skills/reviewer/scripts/run.sh": {Data: []byte("#!/bin/sh\n"), DeclaredExecutable: true},
	}
	// The tree answers from ONE root, so which root the pull asked for is the
	// only thing that can make this fetch succeed. A stub answering whatever it
	// was handed would report the right root no matter where the pull looked.
	var seen []string
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom",
		treeAt(map[string]map[string]TreeFile{".ctxloom/content/bundles/v2/atelier": want}, &seen))

	content, tree, treeRoot, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/v2/atelier", treeTestSHA)

	require.NoError(t, err)
	assert.Contains(t, seen, ".ctxloom/content/bundles/v2/atelier", "the bundle's tree root must be among the roots probed")
	assert.Equal(t, ".ctxloom/content/bundles/v2/atelier", treeRoot, "the root reported is the one that answered")
	assert.Equal(t, "version: \"2.0.0\"\n", string(content), "a tree's bundle.yaml is the bundle's manifest bytes")
	assert.Len(t, tree, 2)
}

// TestFetchItemBytes_RefusesATreeWithNoManifest. A tree with no bundle.yaml
// would install as a pile of files under a bundle's identity that nothing could
// ever load — the silent no-op this codebase is prone to.
func TestFetchItemBytes_RefusesATreeWithNoManifest(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", treeAt(map[string]map[string]TreeFile{
		".ctxloom/content/bundles/v2/atelier": {"fragments/x.md": {Data: []byte("hi")}},
	}, nil))

	_, _, _, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/v2/atelier", treeTestSHA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), BundleManifestName)
}

// TestFetchItemBytes_WithoutAWalkerSaysSo: a puller with no tree fetcher
// cannot read a bundle, and must say that rather than report the bundle
// missing.
func TestFetchItemBytes_WithoutAWalkerSaysSo(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", nil)

	_, _, _, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/v2/atelier", treeTestSHA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tree fetcher")
}

// TestInstallTree_RefusesWithoutAnInstallerRatherThanPinningUnreachableContent.
// Recording a lockfile pin whose tree nothing materialized is this project's
// characteristic silent no-op: the pull reports success, the lock names a
// commit, and every later read fails somewhere that cannot say why.
func TestInstallTree_RefusesWithoutAnInstallerRatherThanPinningUnreachableContent(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", nil)
	ref := treeRef(t)

	_, err := p.installTree(t.Context(), ref, PullOptions{}, &fetchedItem{
		localName: lockKeyOf(t, ref.CanonicalString()),
		treeRoot:  ref.TreeRepoPath(),
		tree:      map[string]TreeFile{BundleManifestName: {Data: []byte("version: \"1.0.0\"\n")}},
	}, treeTestSHA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tree installer")
}

// TestInstallTree_CheckoutsTheWorktreeAtThePinnedCommit pins what the installer
// seam is actually handed. The repo URL, the pinned SHA, the bundle's repository
// path and the worktree directory are the whole of an install, and a wrong value
// in any of them checks out real content in a place no reader looks.
func TestInstallTree_CheckoutsTheWorktreeAtThePinnedCommit(t *testing.T) {
	var gotURL, gotSHA, gotSubpath, gotWorktree string
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", nil)
	WithTreeInstaller(func(_ context.Context, repoURL, sha, subpath, worktreeDir string) (string, error) {
		gotURL, gotSHA, gotSubpath, gotWorktree = repoURL, sha, subpath, worktreeDir
		return filepath.Join(worktreeDir, filepath.FromSlash(subpath)), nil
	})(p)
	ref := treeRef(t)

	dir, err := p.installTree(t.Context(), ref, PullOptions{}, &fetchedItem{
		localName: lockKeyOf(t, ref.CanonicalString()),
		rem:       &Remote{URL: "https://github.com/trent/atelier"},
		sha:       treeTestSHA,
		treeRoot:  ref.TreeRepoPath(),
	}, treeTestSHA)

	require.NoError(t, err)
	assert.Equal(t, "https://github.com/trent/atelier", gotURL)
	assert.Equal(t, treeTestSHA, gotSHA, "the worktree must be detached at the PINNED commit")
	assert.Equal(t, ref.TreeRepoPath(), gotSubpath, "the checkout must be narrowed to the bundle's repository path")
	assert.Equal(t, mustWorktreePath(t, ref, ".ctxloom"), gotWorktree)
	assert.Equal(t, mustTreePath(t, ref, ".ctxloom"), dir,
		"the directory handed back must be the one every reader resolves")
}

// TestInstallTree_RefusesWhenTheFoundRootIsNotTheRootReadersResolve. A checkout
// of a root only the probe knows about lands real bytes where nothing looks,
// and the lockfile records a pin that reads as installed.
func TestInstallTree_RefusesWhenTheFoundRootIsNotTheRootReadersResolve(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", nil)
	called := false
	WithTreeInstaller(func(_ context.Context, _, _, subpath, worktreeDir string) (string, error) {
		called = true
		return filepath.Join(worktreeDir, filepath.FromSlash(subpath)), nil
	})(p)
	ref := treeRef(t)

	_, err := p.installTree(t.Context(), ref, PullOptions{}, &fetchedItem{
		localName: lockKeyOf(t, ref.CanonicalString()),
		rem:       &Remote{URL: "https://github.com/trent/atelier"},
		sha:       treeTestSHA,
		treeRoot:  "bundles/v1/atelier",
	}, treeTestSHA)

	require.Error(t, err)
	assert.False(t, called, "nothing may be checked out at a root readers do not resolve")
}

// TestReadableEntry_RefusesATreeBundleWithAnActionableSentinel. Collapsing this
// into a not-found prints a fix ("run deps pull") that cannot fix anything:
// the pull already succeeded and the bytes are on disk.
func TestReadableEntry_RefusesATreeBundleWithAnActionableSentinel(t *testing.T) {
	var name trust.BundleKey = "https://github.com/trent/atelier@bundles/atelier"
	r := NewBundleReader(nil, nil, AuthConfig{}, &Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{name: {SHA: treeTestSHA}},
	})

	_, err := r.ReadBundleBytes(t.Context(), name)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTreeBundleUnreadable)
	assert.NotErrorIs(t, err, errs.ErrRemoteContentNotFound,
		"a tree bundle that pulled fine must never read as missing remote content")
	assert.Contains(t, err.Error(), treeTestSHA)
}

// stubTreeInstaller stands in for the git checkout in tests whose subject is
// the pull's bookkeeping rather than the checkout itself. It returns the
// directory a real worktree install would return — the bundle at its repository
// path inside the worktree — so LocalPath assertions still mean something.
func stubTreeInstaller() TreeInstallFunc {
	return func(_ context.Context, _, _, subpath, worktreeDir string) (string, error) {
		return filepath.Join(worktreeDir, filepath.FromSlash(subpath)), nil
	}
}
