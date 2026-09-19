package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The headline data-loss defect. `ctxloom deps upgrade` loaded its config
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

// setupSeededLockProject builds a file:// source repo and a project that
// references it from a profile, so the lock can be populated — and it keeps
// that profile OUT of baseDir, in an app dir of its own.
//
// That separation is what makes the empty-closure case reachable. It used to
// come for free: the reference was an INLINE config.yaml definition, so a
// config-load failure took it with the file. With the inline arm retired a
// profile is a file that survives any such failure, so the emptiness has to be
// arranged deliberately — the fallback config points at baseDir, which holds
// the lockfile and no profiles at all.
func setupSeededLockProject(t *testing.T) (baseDir, ref string, cfg *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")

	src := filepath.Join(tmp, "src")
	initLocalRepoWithFile(t, src, repoV1("demo.yaml"), "name: demo\n")
	ref = "file://" + src + "@bundles/demo"

	// baseDir stays AppPaths[0] — that is where the lockfile lives — and the
	// profile goes in a SECOND app dir the loader also searches.
	profileDir := filepath.Join(tmp, "profiles-home", ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.ProfilesPath(profileDir), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(paths.ProfilesPath(profileDir), "onfile.yaml"),
		[]byte("bundles:\n  - "+ref+"\n"), 0o644))

	base := testConfigWithSCMPath(baseDir).ToFixture()
	base.AppPaths = append(base.AppPaths, profileDir)
	cfg = config.NewFixture(base)
	return baseDir, ref, cfg
}

func TestUpgrade_EmptyClosureDoesNotEraseTheLockfile(t *testing.T) {
	baseDir, ref, cfg := setupSeededLockProject(t)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	require.Equal(t, 1, mustLoadActive(t, baseDir).Count(), "the project starts with a populated lock")

	before, err := os.ReadFile(remote.NewLockfileManager(baseDir).Path())
	require.NoError(t, err)

	// The config failed to load: `deps upgrade` proceeds on the empty
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
// state is cleared by `deps upgrade`).
func TestUpgrade_EmptyClosurePreservesHoldsAndRetractions(t *testing.T) {
	baseDir, ref, cfg := setupSeededLockProject(t)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
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
	assert.True(t, after.Held, "the user's hold survives")
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

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
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
	c2 := addFileToLocalRepo(t, srcDirOf(ref), repoV1("demo2.yaml"), "name: demo2\n")
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
//
// Succeeding is not the whole contract, though. An empty closure means NOTHING
// IS DECLARED HERE, which is a different fact from "your declared dependencies
// are all current" — and the caller can only tell them apart if this reports
// it. Nor may the round FABRICATE a lockfile: a file pinning nothing, written
// into a directory that had no lock, is the write half of the same silence.
func TestUpgrade_GenuinelyEmptyProjectStillSucceeds(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	lockPath := remote.NewLockfileManager(baseDir).Path()

	res, err := UpgradeDependencies(context.Background(), testConfigWithSCMPath(baseDir))
	require.NoError(t, err, "an empty project has nothing to upgrade and nothing to lose")
	assert.Equal(t, 0, res.Advanced)
	assert.True(t, res.NothingDeclared,
		"an empty closure is 'nothing is declared here', not 'everything is up to date'")
	_, statErr := os.Stat(lockPath)
	assert.True(t, os.IsNotExist(statErr),
		"a project that declares nothing must not be given a lockfile that pins nothing: %s", lockPath)

	// Same again once an empty lockfile actually exists on disk. The file must
	// be left ALONE — re-stamping LockedAt on a lock that pins nothing records
	// a check that had nothing to check.
	require.NoError(t, remote.NewLockfileManager(baseDir).Save(
		&remote.Lockfile{Version: 1, Bundles: map[string]remote.LockEntry{}}))
	before, err := os.Stat(lockPath)
	require.NoError(t, err)

	res, err = UpgradeDependencies(context.Background(), testConfigWithSCMPath(baseDir))
	require.NoError(t, err)
	assert.Equal(t, 0, res.Advanced)
	assert.True(t, res.NothingDeclared)

	after, err := os.Stat(lockPath)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(),
		"an upgrade with nothing declared must not rewrite the lockfile")
}

// The DISCRIMINATING half of the pair above, and the reason NothingDeclared
// cannot simply be an alias for Advanced==0: a project that really does declare
// a dependency, and whose pin really is current, is the case that still earns
// an unqualified "everything is up to date".
func TestUpgrade_DeclaredAndCurrentIsNotNothingDeclared(t *testing.T) {
	baseDir, _, _, _ := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)

	// Nothing moved upstream, so nothing advances — the same Advanced==0 the
	// empty project produces, from a completely different situation.
	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, 0, res.Advanced)
	assert.False(t, res.NothingDeclared,
		"this project declares a dependency and it is current: that IS 'up to date'")
	assert.False(t, mustLoadActive(t, baseDir).IsEmpty(),
		"the declared pin is still recorded")
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

	// THE FS-THREADING DISCRIMINATOR IS THE require.NoError ABOVE, and it is
	// worth spelling out because it is not obvious.
	//
	// Correctly threaded, `active` is loaded from the injected FS — which has no
	// lock.yaml — so the round resolves nothing, finds nothing to protect, and
	// writes nothing. If EITHER Load or Save fell back to the OS filesystem, it
	// would find the seeded one-entry lock there instead, and an empty closure
	// over a populated lock is precisely what ErrLockfileWouldErase refuses:
	// UpgradeDependencies would have returned an error and the assertion above
	// would be red.
	//
	// This test used to prove the same point by asserting that an empty lock.yaml
	// APPEARED on the injected FS. That evidence was the fabrication bug wearing
	// an instrument's hat — a round that declares nothing now writes nothing, so
	// the absence below is the behaviour and the error-free return is the proof.
	exists, err := afero.Exists(memFS, paths.LockPath(baseDir))
	require.NoError(t, err)
	assert.False(t, exists,
		"a round with nothing declared must not fabricate a lockfile on the injected FS either")
}
