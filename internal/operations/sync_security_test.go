package operations

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written — the "ctxloom: warning:" lines the fault-tolerance paths emit.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	require.NoError(t, w.Close())
	os.Stderr = old
	return <-done
}

// TestSyncDependencies_FirstInstallLandsActive pins the post-demolition model:
// a profile-referenced bundle from an UNTRUSTED remote installs straight into
// the ACTIVE lockfile — the pin moves freely, with no pending-review split.
// Withholding its content from the agent is the content trust gate's job
// (per-item, at exposure), not the lockfile's.
func TestSyncDependencies_FirstInstallLandsActive(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_ = ref

	result, err := SyncDependencies(ctx, cfg, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed, "the first install lands in the active lockfile")

	active := mustLoadActive(t, baseDir)
	e, inActive := active.GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, inActive, "the pin reaches the active lockfile directly")
	assert.Equal(t, c1, e.SHA)
}

// TestLockDependencies_DefaultLockAppliesFirstInstalls pins that a lock applies
// the full closure straight to the active lockfile.
func TestLockDependencies_DefaultLockAppliesFirstInstalls(t *testing.T) {
	baseDir, _, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	assert.Equal(t, "generated", result.Status)
	e, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok)
	assert.Equal(t, c1, e.SHA)
}

// setupRemoteParent builds a file:// repo carrying a bundle that SHIPS a bundle
// profile `parent` (composing a second bundle in the same repo), plus a local
// profile whose parent is that bundle profile. Returns the base dir, the source
// repo dir, and the canonical identities of the parent BUNDLE and the composed
// bundle. (Top-level @profiles/ distribution was retired — a remote parent is a
// bundle profile now, so its closure is discovered through the parent bundle.)
func setupRemoteParent(t *testing.T) (baseDir, src, parentBundleID, bundleID string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src = filepath.Join(tmp, "src")

	bundleID = "file://" + src + "@bundles/demo"
	parentBundleID = "file://" + src + "@bundles/kit"

	initLocalRepoWithFile(t, src, ".ctxloom/content/bundles/demo.yaml", "name: demo\n")
	// The parent bundle ships a bundle profile `parent` that composes demo.
	addFileToLocalRepo(t, src, ".ctxloom/content/bundles/kit.yaml", "version: 1.0.0\nprofiles:\n  parent:\n    bundles:\n      - "+bundleID+"\n")

	writeLocalProfile(t, baseDir, "default", "parents:\n  - "+parentBundleID+"#profiles/parent\n")
	return baseDir, src, parentBundleID, bundleID
}

// TestLockDependencies_UnreachableParentPreservesEntries pins the data-loss
// fix: when a remote parent profile cannot be fetched (clone cache gone +
// upstream unreachable — the offline/transient-failure case), the closure walk
// cannot expand its subtree. The lockfile rebuild must then PRESERVE the
// existing entries under that subtree instead of erasing them, and warn.
func TestLockDependencies_UnreachableParentPreservesEntries(t *testing.T) {
	baseDir, src, parentBundleID, bundleID := setupRemoteParent(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	// Healthy first lock: both the parent bundle and the bundle its profile
	// composes pin.
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	active0 := mustLoadActive(t, baseDir)
	pe0, okP := active0.GetEntry(remote.ItemTypeBundle, parentBundleID)
	be0, okB := active0.GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okP, "remote parent bundle locked")
	require.True(t, okB, "bundle discovered through the bundle-profile parent locked")

	// Simulate the transient failure: the clone cache is gone AND the upstream
	// repo is unreachable, so the parent's content cannot be read anywhere.
	require.NoError(t, os.RemoveAll(paths.ReposCachePath(baseDir)))
	require.NoError(t, os.RemoveAll(src))

	stderr := captureStderr(t, func() {
		result, lerr := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true})
		require.NoError(t, lerr)
		assert.Equal(t, "generated", result.Status)
		assert.Equal(t, 2, result.ItemCount, "both entries survive the incomplete rebuild")
	})
	assert.Contains(t, stderr, "ctxloom: warning:", "the failure is warned, not silent")
	assert.Contains(t, stderr, "could not expand remote parent profile")
	assert.Contains(t, stderr, "preserving")

	active1 := mustLoadActive(t, baseDir)
	pe1, okP := active1.GetEntry(remote.ItemTypeBundle, parentBundleID)
	require.True(t, okP)
	assert.Equal(t, pe0.SHA, pe1.SHA, "the parent bundle itself carries forward from the lock")
	be1, okB := active1.GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "a transient fetch failure must never erase the subtree's lock entries")
	assert.Equal(t, be0.SHA, be1.SHA)
}

// TestUpgrade_UnreachableParentPreservesEntries covers the same data-loss path
// through UpgradeDependencies's wholesale Save(newActive): a reachable bundle
// advances while a remote parent profile is unreachable — the unexpanded
// subtree's entries must survive the rewrite.
func TestUpgrade_UnreachableParentPreservesEntries(t *testing.T) {
	baseDir, src, _, bundleID := setupRemoteParent(t)
	tmp := filepath.Dir(baseDir)

	// A second repo whose advance drives the wholesale rewrite.
	srcA := filepath.Join(tmp, "srcA")
	a1 := initLocalRepoWithFile(t, srcA, ".ctxloom/content/bundles/demoA.yaml", "name: demoA\n")
	refA := "file://" + srcA + "@bundles/demoA"
	writeLocalProfile(t, baseDir, "otherprof", "bundles:\n  - "+refA+"\n")

	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	be0, okB := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "bundle under the remote parent locked")

	// Advance repo A, then make the parent's repo unreachable.
	a2 := addFileToLocalRepo(t, srcA, ".ctxloom/content/bundles/demoA2.yaml", "name: demoA2\n")
	require.NotEqual(t, a1, a2)
	require.NoError(t, os.RemoveAll(paths.ReposCachePath(baseDir)))
	require.NoError(t, os.RemoveAll(src))

	var res UpgradeResult
	stderr := captureStderr(t, func() {
		res, err = UpgradeDependencies(ctx, cfg)
		require.NoError(t, err)
	})
	assert.GreaterOrEqual(t, res.Advanced, 1, "repo A advanced")
	assert.Contains(t, stderr, "could not expand remote parent profile")
	// The caller (runRemoteUpgrade) needs this to avoid claiming
	// "Everything is up to date" on a round where part of the closure was
	// never actually reached.
	assert.True(t, res.Incomplete, "an unreachable parent must be reported as an incomplete closure")

	be1, okB := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "the unexpanded subtree's entry survives the rewrite")
	assert.Equal(t, be0.SHA, be1.SHA)
}

// TestRunSyncPostSteps_FailuresWarnOnStderr pins the project-standard warning
// form for post-sync step failures: a "ctxloom: warning:" line on stderr (the
// structured zap log alone is invisible to a user watching the session start).
func TestRunSyncPostSteps_FailuresWarnOnStderr(t *testing.T) {
	origLock, origHooks := syncLockStep, syncHooksStep
	t.Cleanup(func() { syncLockStep, syncHooksStep = origLock, origHooks })

	syncLockStep = func(context.Context, *config.Config, LockDependenciesRequest) (*LockDependenciesResult, error) {
		return nil, fmt.Errorf("lock boom")
	}
	syncHooksStep = func(context.Context, ApplyHooksRequest) (*ApplyHooksResult, error) {
		return nil, fmt.Errorf("hooks boom")
	}

	stderr := captureStderr(t, func() {
		result := &SyncDependenciesResult{Installed: 1, Total: 1}
		req := SyncDependenciesRequest{Lock: true, ApplyHooks: true}
		runSyncPostSteps(context.Background(), &config.Config{}, req, result, afero.NewMemMapFs())
	})

	assert.Contains(t, stderr, "ctxloom: warning: failed to generate lockfile after sync: lock boom")
	assert.Contains(t, stderr, "ctxloom: warning: failed to apply hooks after sync: hooks boom")
}
