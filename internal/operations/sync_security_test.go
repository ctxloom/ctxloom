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
// was written — the "ctxloom: warning:" lines the fault-tolerance and review
// paths emit.
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

// loadPending returns the pending lockfile for baseDir (empty if absent).
func loadPending(t *testing.T, baseDir string) *remote.Lockfile {
	t.Helper()
	lf, err := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile()).Load()
	require.NoError(t, err)
	return lf
}

// TestSyncDependencies_UntrustedFirstInstallStaged pins the startup-sync
// security gate end to end: a profile-referenced bundle from an UNTRUSTED
// remote that has never been locked is a blind first install — it must be
// staged in the pending lockfile for `ctxloom bundle review`, never written to
// the active lockfile (which is what hooks/MCP/context activation reads), and
// the user must be warned. Approval then activates it.
func TestSyncDependencies_UntrustedFirstInstallStaged(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_ = ref

	var result *SyncDependenciesResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = SyncDependencies(ctx, cfg, SyncDependenciesRequest{Lock: true})
		require.NoError(t, err)
	})

	require.Len(t, result.Staged, 1, "the first install is reported as staged, not installed")
	assert.Equal(t, 0, result.Installed)
	assert.Contains(t, stderr, "staged pending review", "the user is told to run bundle review")

	// SECURITY: the active lockfile must NOT contain the unreviewed bundle.
	active := mustLoadActive(t, baseDir)
	_, inActive := active.GetEntry(remote.ItemTypeBundle, identity)
	assert.False(t, inActive, "an unreviewed first install must not reach the active lockfile")

	pending := loadPending(t, baseDir)
	pe, inPending := pending.GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, inPending, "the first install is staged for review")
	assert.Equal(t, c1, pe.SHA)

	// Approval merges the staged first install into active — only then is the
	// bundle resolvable (and its hooks/MCP applied by the review CLI).
	applied, err := ApproveUpgrade(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, applied)
	e, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok, "approval activates the staged first install")
	assert.Equal(t, c1, e.SHA)
	pendingAfter, err := LoadPendingLockfile(cfg)
	require.NoError(t, err)
	assert.Nil(t, pendingAfter, "pending is cleared after approval")
}

// TestSyncDependencies_TrustedFirstInstallApplies pins the trusted half of the
// gate: a first install from a remote with TrustBundles=true behaves exactly
// as before — straight into the active lockfile, nothing staged.
func TestSyncDependencies_TrustedFirstInstallApplies(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	reg, err := remote.NewRegistry(paths.RemotesPath(baseDir))
	require.NoError(t, err)
	require.NoError(t, reg.Add("src", "file://"+srcDirOf(ref)))
	require.NoError(t, reg.SetTrustBundles("src", true))

	result, err := SyncDependencies(ctx, cfg, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)

	assert.Empty(t, result.Staged, "trusted first installs are not staged")
	assert.Equal(t, 1, result.Installed)

	e, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok, "trusted first install lands in the active lockfile as before")
	assert.Equal(t, c1, e.SHA)

	pending := loadPending(t, baseDir)
	assert.True(t, pending.IsEmpty(), "nothing pends for a trusted remote")
}

// TestLockDependencies_StageUntrustedNewGate pins the lock-side half of the
// startup gate: even when the closure walk discovers an untrusted first
// install (e.g. a bundle referenced by a remote parent profile, which sync
// itself never pulls directly), the startup auto-lock must stage it instead
// of writing it into the active lockfile.
func TestLockDependencies_StageUntrustedNewGate(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_ = ref

	stderr := captureStderr(t, func() {
		result, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, StageUntrustedNew: true})
		require.NoError(t, err)
		assert.Equal(t, "empty", result.Status, "nothing reaches the active lockfile")
	})
	assert.Contains(t, stderr, "staged pending review")

	_, inActive := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	assert.False(t, inActive)
	pe, inPending := loadPending(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, inPending)
	assert.Equal(t, c1, pe.SHA)

	// Once approved into the active lockfile, a later startup lock keeps the
	// entry active — it is no longer a first install.
	applied, err := ApproveUpgrade(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	result, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, StageUntrustedNew: true})
	require.NoError(t, err)
	assert.Equal(t, "generated", result.Status)
	e, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok, "an approved entry stays active across relocks")
	assert.Equal(t, c1, e.SHA)
}

// TestLockDependencies_DefaultLockAppliesFirstInstalls pins that a deliberate,
// explicit lock (StageUntrustedNew unset — not the startup path) keeps its
// historical semantics: the full closure lands in the active lockfile.
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

// setupRemoteParent builds a file:// repo carrying a parent profile that
// references a bundle in the same repo, plus a local profile whose parent is
// that remote profile. Returns the base dir, the source repo dir, and the
// canonical identities of the remote profile and bundle.
func setupRemoteParent(t *testing.T) (baseDir, src, profileID, bundleID string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src = filepath.Join(tmp, "src")

	bundleID = "file://" + src + "@bundles/demo"
	profileID = "file://" + src + "@profiles/parent"

	initLocalRepoWithFile(t, src, "ctxloom/bundles/demo.yaml", "name: demo\n")
	addFileToLocalRepo(t, src, "ctxloom/profiles/parent.yaml", "bundles:\n  - "+bundleID+"\n")

	writeLocalProfile(t, baseDir, "default", "parents:\n  - "+profileID+"\n")
	return baseDir, src, profileID, bundleID
}

// TestLockDependencies_UnreachableParentPreservesEntries pins the data-loss
// fix: when a remote parent profile cannot be fetched (clone cache gone +
// upstream unreachable — the offline/transient-failure case), the closure walk
// cannot expand its subtree. The lockfile rebuild must then PRESERVE the
// existing entries under that subtree instead of erasing them, and warn.
func TestLockDependencies_UnreachableParentPreservesEntries(t *testing.T) {
	baseDir, src, profileID, bundleID := setupRemoteParent(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	// Healthy first lock: both the remote parent profile and its bundle pin.
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	active0 := mustLoadActive(t, baseDir)
	pe0, okP := active0.GetEntry(remote.ItemTypeProfile, profileID)
	be0, okB := active0.GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okP, "remote parent profile locked")
	require.True(t, okB, "bundle discovered through the remote parent locked")

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
	pe1, okP := active1.GetEntry(remote.ItemTypeProfile, profileID)
	require.True(t, okP)
	assert.Equal(t, pe0.SHA, pe1.SHA, "the parent itself carries forward from the lock")
	be1, okB := active1.GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "a transient fetch failure must never erase the subtree's lock entries")
	assert.Equal(t, be0.SHA, be1.SHA)
}

// TestStageUpgrade_UnreachableParentPreservesEntries covers the same data-loss
// path through StageUpgrade's wholesale Save(newActive): a trusted change
// elsewhere triggers the active-lock rewrite while a remote parent profile is
// unreachable — the unexpanded subtree's entries must survive.
func TestStageUpgrade_UnreachableParentPreservesEntries(t *testing.T) {
	baseDir, src, _, bundleID := setupRemoteParent(t)
	tmp := filepath.Dir(baseDir)

	// A second, TRUSTED repo whose advance triggers the wholesale rewrite.
	srcA := filepath.Join(tmp, "srcA")
	a1 := initLocalRepoWithFile(t, srcA, "ctxloom/bundles/demoA.yaml", "name: demoA\n")
	refA := "file://" + srcA + "@bundles/demoA"
	writeLocalProfile(t, baseDir, "trustedprof", "bundles:\n  - "+refA+"\n")

	cfg := testConfigWithSCMPath(baseDir)
	reg, err := remote.NewRegistry(paths.RemotesPath(baseDir))
	require.NoError(t, err)
	require.NoError(t, reg.Add("repoA", "file://"+srcA))
	require.NoError(t, reg.SetTrustBundles("repoA", true))

	ctx := context.Background()
	_, err = LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	be0, okB := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "bundle under the remote parent locked")

	// Advance the trusted repo, then make the parent's repo unreachable.
	a2 := addFileToLocalRepo(t, srcA, "ctxloom/bundles/demoA2.yaml", "name: demoA2\n")
	require.NotEqual(t, a1, a2)
	require.NoError(t, os.RemoveAll(paths.ReposCachePath(baseDir)))
	require.NoError(t, os.RemoveAll(src))

	var res UpgradeResult
	stderr := captureStderr(t, func() {
		res, err = StageUpgrade(ctx, cfg)
		require.NoError(t, err)
	})
	require.Equal(t, 1, res.TrustedApplied, "the trusted advance triggers the wholesale rewrite")
	assert.Contains(t, stderr, "could not expand remote parent profile")

	be1, okB := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "the unexpanded subtree's entry survives the trusted rewrite")
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
	syncHooksStep = func(context.Context, *config.Config, ApplyHooksRequest) (*ApplyHooksResult, error) {
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
