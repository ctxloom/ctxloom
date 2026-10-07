package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/ident"
)

func lockOf(entries map[ident.BundleKey]remote.LockEntry) *remote.Lockfile {
	return &remote.Lockfile{Version: remote.LockfileVersion, Bundles: entries}
}

func TestPreserveUnreachedEntries_CarriesUnreachedAndKeepsReached(t *testing.T) {
	warnings := captureWarnings(t)
	active := lockOf(map[ident.BundleKey]remote.LockEntry{
		"https://github.com/o/r@bundles/reached":   {SHA: "old", URL: "https://github.com/o/r"},
		"https://github.com/o/r@bundles/unreached": {SHA: "kept", URL: "https://github.com/o/r", Held: true},
	})
	next := lockOf(map[ident.BundleKey]remote.LockEntry{
		"https://github.com/o/r@bundles/reached": {SHA: "new", URL: "https://github.com/o/r"},
	})

	preserveUnreachedEntries(active, next, 2)

	reached, _ := next.GetEntry(remote.ItemTypeBundle, "https://github.com/o/r@bundles/reached")
	assert.Equal(t, "new", reached.SHA, "an entry the new lock reached is never overwritten by the old one")
	carried, ok := next.GetEntry(remote.ItemTypeBundle, "https://github.com/o/r@bundles/unreached")
	require.True(t, ok, "an entry the new lock did not reach is carried")
	assert.Equal(t, remote.LockEntry{SHA: "kept", URL: "https://github.com/o/r", Held: true}, carried, "carried verbatim")
	assert.Contains(t, warnings.String(), "2 parent profile(s) unreachable")
	assert.Contains(t, warnings.String(), "preserving 1 existing lockfile entry(ies)")
}

func TestPreserveUnreachedEntries_SilentWhenNothingCarried(t *testing.T) {
	warnings := captureWarnings(t)
	active := lockOf(map[ident.BundleKey]remote.LockEntry{
		"https://github.com/o/r@bundles/reached": {SHA: "old"},
	})
	next := lockOf(map[ident.BundleKey]remote.LockEntry{
		"https://github.com/o/r@bundles/reached": {SHA: "new"},
	})

	preserveUnreachedEntries(active, next, 1)

	assert.Empty(t, warnings.String(), "nothing carried, nothing to warn about")
	assert.Equal(t, 1, next.Count())
}

// setupIncompleteClosure is a locked project whose closure then becomes
// INCOMPLETE: its profile gains a parent that cannot be expanded, and the lock
// holds an extra entry the remaining closure no longer reaches. Only the
// incomplete-closure carry keeps that entry.
func setupIncompleteClosure(t *testing.T) (baseDir string, orphan ident.BundleKey) {
	t.Helper()
	baseDir, ref, _, _ := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)

	orphan = lockKeyOf(t, "file://"+srcDirOf(ref)+"@bundles/orphan")
	mgr := remote.NewLockfileManager(baseDir)
	lf, err := mgr.Load()
	require.NoError(t, err)
	lf.AddEntry(remote.ItemTypeBundle, orphan, remote.LockEntry{SHA: "0123456789abcdef0123456789abcdef01234567", URL: "file://" + srcDirOf(ref)})
	require.NoError(t, mgr.Save(lf))

	writeLocalProfile(t, baseDir, "default", "parents:\n  - ctxloom:local@bundles/does-not-exist\nbundles:\n  - "+ref+"\n")
	return baseDir, orphan
}

func TestLockDependencies_IncompleteClosurePreservesUnreachedEntries(t *testing.T) {
	baseDir, orphan := setupIncompleteClosure(t)
	warnings := captureWarnings(t)

	res, err := LockDependencies(context.Background(), testConfigWithSCMPath(baseDir), LockDependenciesRequest{})
	require.NoError(t, err)

	assert.True(t, res.Incomplete)
	_, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, orphan)
	assert.True(t, ok, "an incomplete relock keeps the entry it could not reach")
	assert.Contains(t, warnings.String(), "preserving 1 existing lockfile entry(ies)")
}

func TestUpgradeDependencies_IncompleteClosurePreservesUnreachedEntries(t *testing.T) {
	baseDir, orphan := setupIncompleteClosure(t)
	warnings := captureWarnings(t)

	res, err := UpgradeDependencies(context.Background(), testConfigWithSCMPath(baseDir), UpgradeRequest{Apply: true})
	require.NoError(t, err)

	assert.True(t, res.Incomplete)
	assert.Empty(t, res.Removed, "the unreached entry is not reported as dropped")
	_, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, orphan)
	assert.True(t, ok, "an incomplete upgrade keeps the entry it could not reach")
	assert.Contains(t, warnings.String(), "preserving 1 existing lockfile entry(ies)")
}
