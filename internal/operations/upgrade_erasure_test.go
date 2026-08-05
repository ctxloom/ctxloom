package operations

import (
	"context"
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

// The headline data-loss defect. `ctxloom remote upgrade` loaded its config
// through loadConfigOrFallback, a FAULT-TOLERANT helper written for read-only
// commands: on any config-load error it hands back a minimal EMPTY config. An
// empty config has no profile definitions, so closureRoots enumerates nothing,
// the resolved closure is empty, the carry-forward guard (which only fires on
// an INCOMPLETE closure) never runs, and the unconditional Save wrote an empty
// lockfile over a fully populated one — then printed "Everything is up to
// date." and exited 0.
//
// fallbackShapedConfig is exactly what loadConfigOrFallback returns
// (config.NewFixture with AppPaths only), pointed at the project's app dir:
// no inline profile definitions, no defaults, nothing.
func fallbackShapedConfig(baseDir string) *config.Config {
	return config.NewFixture(config.Fixture{AppPaths: []string{baseDir}})
}

// setupInlineOnlyProject builds a file:// source repo and a project whose ONLY
// reference to it is an INLINE config.yaml profile definition. That is the
// shape a config-load failure erases: directory profiles survive on disk, but
// inline definitions live in the very file that failed to load.
func setupInlineOnlyProject(t *testing.T) (baseDir, ref string, cfg *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")

	src := filepath.Join(tmp, "src")
	initLocalRepoWithFile(t, src, ".ctxloom/content/bundles/demo.yaml", "name: demo\n")
	ref = "file://" + src + "@bundles/demo"

	f := testConfigWithSCMPath(baseDir).ToFixture()
	f.Profiles = config.ProfilesConfig{Definitions: map[string]config.Profile{
		"inline": {Bundles: []string{ref}},
	}}
	return baseDir, ref, config.NewFixture(f)
}

func TestUpgrade_EmptyClosureDoesNotEraseTheLockfile(t *testing.T) {
	baseDir, ref, cfg := setupInlineOnlyProject(t)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	require.Equal(t, 1, mustLoadActive(t, baseDir).Count(), "the project starts with a populated lock")

	before, err := os.ReadFile(remote.NewLockfileManager(baseDir).Path())
	require.NoError(t, err)

	// The config failed to load: `remote upgrade` proceeds on the empty
	// fallback, so the closure is empty and there is nothing to propose.
	_, err = UpgradeDependencies(ctx, fallbackShapedConfig(baseDir))
	require.Error(t, err, "an upgrade that resolved no dependencies must fail, not silently erase the lock")
	assert.ErrorIs(t, err, remote.ErrLockfileWouldErase)

	after, err := os.ReadFile(remote.NewLockfileManager(baseDir).Path())
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the lockfile is byte-identical after the refused upgrade")

	entry, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	assert.True(t, ok, "the dependency pin survives")
	assert.NotEmpty(t, entry.SHA)
}

// The security-relevant payload: a wipe silently un-holds every hold and, far
// worse, UN-RETRACTS content the publisher withdrew (retraction
// state is cleared by `remote upgrade`).
func TestUpgrade_EmptyClosurePreservesHoldsAndRetractions(t *testing.T) {
	baseDir, ref, cfg := setupInlineOnlyProject(t)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	// Hold the bundle, and record a publisher retraction against it.
	held, err := SetItemPin(cfg, ref, true)
	require.NoError(t, err)
	require.True(t, held)

	mgr := remote.NewLockfileManager(baseDir)
	lf, err := mgr.Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok)
	entry.Retracted = true
	entry.RetractedReason = "withdrawn by the publisher"
	lf.AddEntry(remote.ItemTypeBundle, ref, entry)
	require.NoError(t, mgr.Save(lf))

	_, err = UpgradeDependencies(ctx, fallbackShapedConfig(baseDir))
	require.Error(t, err)

	after, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok)
	assert.True(t, after.Pinned, "the user's hold survives")
	assert.True(t, after.Retracted, "the publisher's retraction survives — a wipe would silently un-retract it")
	assert.Equal(t, "withdrawn by the publisher", after.RetractedReason)
}

// TestUpgrade_RetractionSurvivesNonEmptyReresolve pins that a full
// re-resolve that DOES produce results (unlike the empty-closure case above)
// must not silently un-retract an item the publisher withdrew. Only a fresh
// CheckRetracted (sync's installed-ref re-check, or the next Pull) is
// entitled to lift a retraction — a wholesale relock is not a fresh check,
// yet the entry UpgradeDependencies builds for every non-held, re-proposed
// item started from a zero value and dropped Retracted/RetractedReason on
// the floor.
func TestUpgrade_RetractionSurvivesNonEmptyReresolve(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	// Record a publisher retraction against the entry, WITHOUT holding it —
	// holding takes a different (already-correct) code path in
	// UpgradeDependencies that copies `cur` wholesale.
	mgr := remote.NewLockfileManager(baseDir)
	lf, err := mgr.Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok)
	entry.Retracted = true
	entry.RetractedReason = "withdrawn by the publisher"
	lf.AddEntry(remote.ItemTypeBundle, identity, entry)
	require.NoError(t, mgr.Save(lf))

	// Advance upstream so the re-resolve is non-empty and genuinely proposes a
	// move — this is the case the empty-closure guard (Save's
	// ErrLockfileWouldErase) does not cover at all.
	c2 := addFileToLocalRepo(t, srcDirOf(ref), ".ctxloom/content/bundles/demo2.yaml", "name: demo2\n")
	require.NotEqual(t, c1, c2)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Advanced)

	after, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, identity)
	require.True(t, ok)
	assert.Equal(t, c2, after.SHA, "the SHA still advances")
	assert.True(t, after.Retracted, "a wholesale re-resolve must not silently un-retract content the publisher withdrew")
	assert.Equal(t, "withdrawn by the publisher", after.RetractedReason)
}

// The legitimate empty case: a project with genuinely nothing pinned upgrades
// to nothing, successfully. Replacing a data-loss bug with a usability one is
// not a fix.
func TestUpgrade_GenuinelyEmptyProjectStillSucceeds(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))

	res, err := UpgradeDependencies(context.Background(), testConfigWithSCMPath(baseDir))
	require.NoError(t, err, "an empty project has nothing to upgrade and nothing to lose")
	assert.Equal(t, 0, res.Advanced)

	// Same again once an empty lockfile actually exists on disk.
	res, err = UpgradeDependencies(context.Background(), testConfigWithSCMPath(baseDir))
	require.NoError(t, err)
	assert.Equal(t, 0, res.Advanced)
}

// TestUpgrade_HonoursInjectedLockfileFS pins that UpgradeDependencies must
// read AND write the lockfile through cfg.FS() when one is injected, not
// silently fall back to the real OS filesystem for one half of the
// read-modify-write. Before the fix, both NewLockfileManager
// constructions here omitted WithLockfileFS, so an injected FS's lock.yaml was
// invisible to Load/Save even though profileLoader/closure resolution (via
// cfg) is FS-scoped elsewhere — the exact mismatch the reviewer called the
// most plausible trigger of a HIGH lockfile-erasure finding.
func TestUpgrade_HonoursInjectedLockfileFS(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))

	// Seed a REAL, populated lock.yaml directly on the OS filesystem — the
	// wrong place for this call to touch once an FS is injected.
	osLock := &remote.Lockfile{Version: 1, Bundles: map[string]remote.LockEntry{
		"https://github.com/o/r@bundles/demo": {SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", URL: "https://github.com/o/r"},
	}}
	require.NoError(t, remote.NewLockfileManager(baseDir).Save(osLock))

	// cfg has no profiles, so the closure is empty and the proposed set is
	// empty — this isolates the FS-threading question from closure
	// resolution. cfg.FS() points at an isolated in-memory filesystem with NO
	// lock.yaml on it at all.
	cfg := testConfigWithSCMPath(baseDir)
	memFS := afero.NewMemMapFs()
	cfg.SetFS(memFS)

	_, err := UpgradeDependencies(context.Background(), cfg)
	require.NoError(t, err)

	// The OS-disk lockfile must be untouched: still 1 entry, same SHA.
	onDisk, err := remote.NewLockfileManager(baseDir).Load()
	require.NoError(t, err)
	entry, ok := onDisk.GetEntry(remote.ItemTypeBundle, "https://github.com/o/r@bundles/demo")
	require.True(t, ok, "UpgradeDependencies must not touch the real OS lockfile when an FS is injected")
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", entry.SHA)

	// The injected in-memory FS must now hold ITS OWN (empty) lock.yaml — the
	// write actually landed where cfg said it should.
	exists, err := afero.Exists(memFS, paths.LockPath(baseDir))
	require.NoError(t, err)
	assert.True(t, exists, "UpgradeDependencies must write the lockfile through the injected FS, not the OS filesystem")
}
