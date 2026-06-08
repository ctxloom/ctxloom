package operations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// setupUpgrade builds a file:// source repo with one commit and a local profile
// that tracks its default branch (a version-less constraint). It returns the
// base dir, the manifest ref, the bundle's canonical identity, and commit one.
func setupUpgrade(t *testing.T) (cfgBase string, ref, identity, c1 string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")

	src := filepath.Join(tmp, "src")
	c1 = initLocalRepoWithFile(t, src, "ctxloom/bundles/demo.yaml", "name: demo\n")
	ref = "file://" + src + "@bundles/demo" // version-less → track default branch
	identity = ref

	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")
	return baseDir, ref, identity, c1
}

func TestStageUpgrade_StagesUntrustedAdvance_LockOnly(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	// Initial lock pins the current default-branch tip.
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	e0, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok, "the bundle is locked")
	require.Equal(t, c1, e0.SHA)

	// Advance the branch upstream, then upgrade.
	c2 := addFileToLocalRepo(t, srcDirOf(ref), "ctxloom/bundles/demo2.yaml", "name: demo2\n")
	require.NotEqual(t, c1, c2)

	res, err := StageUpgrade(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, res.TrustedApplied, "an unregistered remote is untrusted → nothing auto-applied")
	assert.Equal(t, 1, res.Staged)

	// Active still at c1; the new SHA is staged in pending.
	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	assert.Equal(t, c1, e1.SHA, "an untrusted upgrade leaves the active lock untouched")

	pending, err := LoadPendingLockfile(cfg)
	require.NoError(t, err)
	require.NotNil(t, pending)
	pe, ok := pending.GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok)
	assert.Equal(t, c2, pe.SHA, "the new SHA is staged for review")

	// Approve merges pending → active.
	applied, err := ApproveUpgrade(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, applied)
	e2, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	assert.Equal(t, c2, e2.SHA, "approval advances the active lock")

	// The manifest still holds the bare constraint — never rewritten.
	loaded, err := profileLoader(cfg).Load("default")
	require.NoError(t, err)
	assert.Equal(t, []string{ref}, loaded.Bundles, "upgrade/approve never rewrite the manifest ref")
}

func TestStageUpgrade_HeldEntryDoesNotAdvance(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	// Hold the bundle at its current SHA.
	held, err := SetItemPin(cfg, ref, true)
	require.NoError(t, err)
	require.True(t, held)

	// Advance upstream, then upgrade — the held entry must not move.
	c2 := addFileToLocalRepo(t, srcDirOf(ref), "ctxloom/bundles/demo2.yaml", "name: demo2\n")
	require.NotEqual(t, c1, c2)

	res, err := StageUpgrade(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Staged, "a held entry is never staged for upgrade")
	assert.Equal(t, 0, res.TrustedApplied)

	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	assert.Equal(t, c1, e1.SHA, "the held entry stays frozen at its locked SHA")
}

func mustLoadActive(t *testing.T, baseDir string) *remote.Lockfile {
	t.Helper()
	lf, err := remote.NewLockfileManager(baseDir).Load()
	require.NoError(t, err)
	return lf
}

// srcDirOf extracts the source repo directory from a "file://<dir>@bundles/x" ref.
func srcDirOf(ref string) string {
	s := strings.TrimPrefix(ref, "file://")
	if i := strings.Index(s, "@"); i >= 0 {
		return s[:i]
	}
	return s
}
