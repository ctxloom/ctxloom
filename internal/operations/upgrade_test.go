package operations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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
	c1 = initLocalRepoWithFile(t, src, ".ctxloom/content/bundles/demo.yaml", "name: demo\n")
	ref = "file://" + src + "@bundles/demo" // version-less → track default branch
	identity = ref

	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")
	return baseDir, ref, identity, c1
}

// TestUpgrade_AdvancesActiveLock pins the new model: an upgrade re-resolves the
// closure and writes the new SHA straight to the ACTIVE lock — there is no
// pending-review split. Whether the new content ever reaches the agent is
// decided per item at exposure by the content trust gate, not here.
func TestUpgrade_AdvancesActiveLock(t *testing.T) {
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
	c2 := addFileToLocalRepo(t, srcDirOf(ref), ".ctxloom/content/bundles/demo2.yaml", "name: demo2\n")
	require.NotEqual(t, c1, c2)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Advanced)

	// The active lock now holds the new SHA — no approval step.
	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	assert.Equal(t, c2, e1.SHA, "the upgrade advances the active lock directly")

	// The manifest still holds the bare constraint — never rewritten.
	loaded, err := profileLoader(cfg).Load("default")
	require.NoError(t, err)
	assert.Equal(t, []string{ref}, loaded.Bundles, "upgrade never rewrites the manifest ref")
}

func TestUpgrade_HeldEntryDoesNotAdvance(t *testing.T) {
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
	c2 := addFileToLocalRepo(t, srcDirOf(ref), ".ctxloom/content/bundles/demo2.yaml", "name: demo2\n")
	require.NotEqual(t, c1, c2)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Advanced, "a held entry is never advanced by upgrade")

	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	assert.Equal(t, c1, e1.SHA, "the held entry stays frozen at its locked SHA")
	assert.True(t, e1.Held, "the hold survives the relock")
}

// TestUpgrade_PreservesInlineRootedEntry pins the canonical-root-set fix: the
// wholesale active-lock rewrite an upgrade performs must not erase a dependency
// rooted ONLY in an inline config.yaml profile. A narrower root set (directory
// profiles only) dropped such entries.
func TestUpgrade_PreservesInlineRootedEntry(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")

	// Bundle in repo A, referenced by a directory profile.
	srcA := filepath.Join(tmp, "srcA")
	a1 := initLocalRepoWithFile(t, srcA, ".ctxloom/content/bundles/demoA.yaml", "name: demoA\n")
	refA := "file://" + srcA + "@bundles/demoA"
	writeLocalProfile(t, baseDir, "dirprof", "bundles:\n  - "+refA+"\n")

	// Bundle in repo B, referenced ONLY by an inline config.yaml definition.
	srcB := filepath.Join(tmp, "srcB")
	b1 := initLocalRepoWithFile(t, srcB, ".ctxloom/content/bundles/demoB.yaml", "name: demoB\n")
	refB := "file://" + srcB + "@bundles/demoB"

	f := testConfigWithSCMPath(baseDir).ToFixture()
	f.Profiles = config.ProfilesConfig{Definitions: map[string]config.Profile{
		"inlineprof": {Bundles: []string{refB}},
	}}
	cfg := config.NewFixture(f)

	ctx := context.Background()
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	active0 := mustLoadActive(t, baseDir)
	_, okA := active0.GetEntry(remote.ItemTypeBundle, refA)
	eB0, okB := active0.GetEntry(remote.ItemTypeBundle, refB)
	require.True(t, okA, "repo-A bundle locked")
	require.True(t, okB, "inline-rooted bundle locked")
	require.Equal(t, b1, eB0.SHA)

	// Advance repo A and upgrade: the active lock is rewritten from the closure.
	a2 := addFileToLocalRepo(t, srcA, ".ctxloom/content/bundles/demoA2.yaml", "name: demoA2\n")
	require.NotEqual(t, a1, a2)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, 1, res.Advanced, "repo A advanced")

	// The inline-rooted entry must survive the wholesale rewrite.
	active1 := mustLoadActive(t, baseDir)
	eB1, ok := active1.GetEntry(remote.ItemTypeBundle, refB)
	assert.True(t, ok, "inline-config-rooted entry must survive the active-lock rewrite")
	assert.Equal(t, b1, eB1.SHA)
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

// unionLockedRepoURLs must cover every repo recorded in the active lock so
// refreshRepoCaches advances every clone the closure walk will read — not just
// the roots' direct repos.
func TestUnionLockedRepoURLs(t *testing.T) {
	lock := &remote.Lockfile{
		Bundles: map[string]remote.LockEntry{
			"https://github.com/a/r@bundles/x":  {URL: "https://github.com/a/r"},
			"https://github.com/b/r@bundles/y":  {URL: "https://github.com/b/r"},
			"https://github.com/b/r@bundles/y2": {URL: "https://github.com/b/r"}, // same repo, dedup'd
			"https://github.com/c/r@bundles/p":  {URL: "https://github.com/c/r"},
			"local/no-url":                      {}, // URL-less entries are skipped
		},
	}
	got := unionLockedRepoURLs([]string{"https://github.com/a/r"}, lock)
	assert.ElementsMatch(t, []string{
		"https://github.com/a/r", // direct (kept once despite also being locked)
		"https://github.com/b/r",
		"https://github.com/c/r",
	}, got)

	assert.Equal(t, []string{"https://github.com/x/y"}, unionLockedRepoURLs([]string{"https://github.com/x/y"}, nil),
		"nil lock leaves the direct set untouched")
}
