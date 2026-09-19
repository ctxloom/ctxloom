package operations

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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

	result, err := SyncDependencies(ctx, fixtureApp(t, cfg), SyncDependenciesRequest{Lock: true})
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

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
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
	return setupRemoteParentSigned(t, true)
}

// setupRemoteParentSigned is setupRemoteParent with the publisher's signature
// made optional, so the trust gate can be exercised from both sides by ONE
// fixture. A second fixture that drifted from this one would let the signed and
// unsigned cases stop being the same bundle, which is the only thing that makes
// comparing their outcomes meaningful.
func setupRemoteParentSigned(t *testing.T, signed bool) (baseDir, src, parentBundleID, bundleID string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src = filepath.Join(tmp, "src")

	bundleID = "file://" + src + "@bundles/demo"
	parentBundleID = "file://" + src + "@bundles/kit"

	// TREE form, which is the only form published since the v1 removal: a
	// bundle is a DIRECTORY whose bundle.yaml is its manifest. Expanding a
	// remote bundle-profile parent (depWalker.recurseBundleProfile) therefore
	// goes through bundles.ReadRemoteRef's tree path — authoring these as
	// documents tested a shape no repository can publish any more.
	// Both bundles are authored as PUBLISHABLE trees: bundle.yaml carries
	// envelope keys ONLY, and every item is a file beside it. bundles.ReadTree
	// refuses both halves of any other shape — an envelope that also declares
	// items inline ("two answers for one item"), and a tree with no item files
	// at all ("declares no items") — so demo needs a real item of its own even
	// though the test only ever asks whether it was PINNED.
	initLocalRepoWithFile(t, src, repoV2("demo")+"/bundle.yaml", "name: demo\n")
	addFileToLocalRepo(t, src, repoV2("demo")+"/fragments/note.md", "demo fragment body\n")
	// The parent bundle ships a bundle profile `parent` that composes demo, as
	// a TREE ITEM FILE — profiles/<name>.yaml, whose body is the profile def
	// and whose name is the filename. This is what convert.Convert emits and
	// the only shape a publisher can publish; an inline `profiles:` key here
	// would be refused by readEnvelope.
	addFileToLocalRepo(t, src, repoV2("kit")+"/bundle.yaml", "version: 1.0.0\n")
	addFileToLocalRepo(t, src, repoV2("kit")+"/profiles/parent.yaml", "bundles:\n  - "+bundleID+"\n")

	// Both trees are SIGNED, and the publisher is trusted. Reading a remote
	// tree's item files means interpreting publisher-supplied bytes, so
	// bundles.ReadRemoteRef verifies the whole bundle before ReadTree — an
	// unsigned tree is refused rather than half-read. Signing goes through the
	// production attest.SignBundle so the fixture proves the real publish path
	// produces something the real read path accepts.
	if signed {
		signer := testSigner(t)
		signTreeAndCommit(t, src, "demo", signer)
		signTreeAndCommit(t, src, "kit", signer)
		trustPublisher(t, baseDir, signer)
	}

	writeLocalProfile(t, baseDir, "default", "parents:\n  - "+parentBundleID+"#profiles/parent\n")
	return baseDir, src, parentBundleID, bundleID
}

// signTreeAndCommit signs the tree bundle named bundleName inside the repo's
// bundles root with signer, then commits every file signing produced (the
// SHA256SUMS manifest and the signature store beside it) and returns the
// resulting commit SHA.
//
// It signs through content.TreeStore + attest.SignBundle — the same objects the
// consumer verifies through — rather than writing manifest and signature paths
// by hand. A fixture that hand-rolled those paths would be asserting against
// this test's idea of the on-disk layout instead of the product's.
func signTreeAndCommit(t *testing.T, repoDir, bundleName string, signer ssh.Signer) string {
	t.Helper()
	ctx := context.Background()
	bundlesRoot := filepath.Join(repoDir, filepath.FromSlash(paths.RepoBundlesPrefixFor(paths.LayoutV2)))
	store, err := content.NewTreeStore(afero.NewOsFs(), bundlesRoot, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	tree, err := store.Open(ctx, content.BundleID(bundleName))
	require.NoError(t, err)
	require.NoError(t, attest.SignBundle(ctx, store, tree, signer))

	repo, err := git.PlainOpen(repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	sha, err := wt.Commit("sign "+bundleName, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return sha.String()
}

// TestLockDependencies_TreeFormParentExpandsTheClosure pins the capability
// childlike-failing named as dead: expanding a remote BUNDLE-PROFILE PARENT
// that is published as a DIRECTORY. `demo` is reachable ONLY through the
// `parent` profile shipped inside the `kit` bundle, so its presence in the
// lockfile is proof the walk read kit's manifest out of the tree and followed
// what it composes. Without a whole-tree read the walk
// degrades to markUnexpanded plus a warning and the command still reports
// success — a silently INCOMPLETE closure, which is the failure this asserts
// against.
func TestLockDependencies_TreeFormParentExpandsTheClosure(t *testing.T) {
	baseDir, _, parentBundleID, bundleID := setupRemoteParent(t)
	cfg := testConfigWithSCMPath(baseDir)

	stderr := captureStderr(t, func() {
		result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
		require.NoError(t, err)
		assert.Equal(t, "generated", result.Status)
	})
	assert.NotContains(t, stderr, "could not expand remote parent profile",
		"a tree-form parent must EXPAND, not degrade to an unexpanded subtree")

	active := mustLoadActive(t, baseDir)
	_, okP := active.GetEntry(remote.ItemTypeBundle, parentBundleID)
	require.True(t, okP, "the tree-form parent bundle itself is pinned")
	_, okB := active.GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB,
		"the bundle composed by the parent's profile is discoverable ONLY by reading that profile out of the tree")
}

// TestLockDependencies_UnsignedTreeParentIsRefusedNotSilentlyExpanded is the
// other half of the gate its signed twin above proves fires: the SAME fixture,
// unsigned, must be REFUSED — and must say so.
//
// It exists because this defect's whole history is a gate nobody watched fire.
// The verification added to the remote read path would be indistinguishable
// from no verification at all if only the passing case were pinned: a check
// that silently admitted everything would keep the twin above green.
//
// WHAT IT ASSERTS, and why each half is needed:
//
//   - The refusal names TRUST. The failure this replaced said "bundle has no
//     profile", which blamed a publisher for shipping a correct bundle and sent
//     the reader to re-author a file that was never wrong. A refusal an operator
//     cannot act on is barely better than the silence it replaced.
//   - The closure is INCOMPLETE rather than quietly complete. What was reachable
//     only through the unverified parent must not be pinned — that is the
//     content the gate exists to keep out.
//
// The parent BUNDLE itself still pins, and that is not an oversight: the walk
// records it before it reads it, so the pin reflects what the profile declared,
// not what the tree turned out to contain. Asserting it here keeps that ordering
// visible rather than letting a future change quietly alter it.
func TestLockDependencies_UnsignedTreeParentIsRefusedNotSilentlyExpanded(t *testing.T) {
	baseDir, _, parentBundleID, bundleID := setupRemoteParentSigned(t, false)
	cfg := testConfigWithSCMPath(baseDir)

	stderr := captureStderr(t, func() {
		result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
		require.NoError(t, err)
		assert.Equal(t, "generated", result.Status)
	})

	assert.Contains(t, stderr, "could not expand remote parent profile",
		"an unverifiable parent must be REPORTED, not silently skipped")
	assert.Contains(t, stderr, "unattested",
		"the diagnosis must name the TRUST failure — the reader has to know a signature is missing, not a profile")
	assert.NotContains(t, stderr, "bundle has no profile",
		"blaming the publisher for a correct bundle is the misdiagnosis this whole path was rebuilt to remove")

	active := mustLoadActive(t, baseDir)
	_, okP := active.GetEntry(remote.ItemTypeBundle, parentBundleID)
	assert.True(t, okP, "the parent bundle is recorded before it is read, so its own pin still lands")
	_, okB := active.GetEntry(remote.ItemTypeBundle, bundleID)
	assert.False(t, okB,
		"a bundle reachable ONLY through an unverified parent's profile must not be pinned — admitting it is exactly what the gate exists to prevent")
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
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
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
		result, lerr := LockDependencies(ctx, cfg, LockDependenciesRequest{})
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

	// A second repo whose advance drives the wholesale rewrite. Tree form: its
	// pin advances below, and verifyAdvance reads through the tree at the
	// proposed SHA.
	srcA := filepath.Join(tmp, "srcA")
	a1 := initLocalRepoWithFile(t, srcA, repoV2("demoA")+"/bundle.yaml", "name: demoA\n")
	refA := "file://" + srcA + "@bundles/demoA"
	writeLocalProfile(t, baseDir, "otherprof", "bundles:\n  - "+refA+"\n")

	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	be0, okB := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, bundleID)
	require.True(t, okB, "bundle under the remote parent locked")

	// Advance repo A, then make the parent's repo unreachable.
	a2 := addFileToLocalRepo(t, srcA, repoV2("demoA2"), "name: demoA2\n")
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
