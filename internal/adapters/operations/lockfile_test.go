// Lockfile operation tests verify dependency locking for reproducible installations.
// Lockfiles capture exact SHA versions of installed remote items, enabling teams to
// share consistent ctxloom configurations and enabling CI/CD reproducibility.
package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// =============================================================================
// Request/Result Structure Tests
// =============================================================================
// These verify the data structures used for lockfile operations.

func TestLockDependenciesRequest_FSField(t *testing.T) {
	fs := afero.NewMemMapFs()
	req := LockDependenciesRequest{
		FS: fs,
	}

	assert.NotNil(t, req.FS)
}

func TestLockDependenciesResult_Fields(t *testing.T) {
	result := LockDependenciesResult{
		Status:    "generated",
		Path:      paths.LockPath(testBaseDir),
		ItemCount: 5,
		Message:   "",
	}

	assert.Equal(t, "generated", result.Status)
	assert.Contains(t, result.Path, paths.LockFileName)
	assert.Equal(t, 5, result.ItemCount)
}

func TestLockDependenciesResult_EmptyStatus(t *testing.T) {
	result := LockDependenciesResult{
		Status:  "empty",
		Message: "No remote items with source metadata found",
	}

	assert.Equal(t, "empty", result.Status)
	assert.NotEmpty(t, result.Message)
}

// =============================================================================
// LockDependencies Integration Tests
// =============================================================================
// Lock builds lock.yaml from the flattened, hash-pinned transitive closure of
// the project's local profiles, surfacing a same-item/different-hash conflict
// immediately. (Uses real temp dirs so the profile loader reads files.)

// writeLocalProfile writes a local profile file under baseDir/profiles.
func writeLocalProfile(t *testing.T, baseDir, name, body string) {
	t.Helper()
	dir := bundletree.ProjectProfilesDir(t, baseDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644))
}

func TestLockDependencies_NoProfiles(t *testing.T) {
	cfg := testConfigWithSCMPath(t.TempDir())

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{})
	require.NoError(t, err)

	assert.Equal(t, "empty", result.Status)
	assert.Contains(t, result.Message, "No remote items")
	assert.False(t, result.Incomplete, "a project with no profiles is genuinely empty")
}

// An empty lock built from a closure that could not be read is not "no
// remote items": the items were never looked at. Incomplete is what tells a
// caller the two apart.
func TestLockDependencies_UnreachableParentIsAnIncompleteEmptyLock(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")
	missing := filepath.Join(tmp, "no-such-repo")
	writeLocalProfile(t, baseDir, "default", "parents:\n  - file://"+missing+"@bundles/kit#profiles/parent\n")
	registerTestRemote(t, baseDir, "file://"+missing)
	cfg := withOnDiskRoot(t, testConfigWithSCMPath(baseDir), baseDir)

	var result *LockDependenciesResult
	captureStderr(t, func() {
		var err error
		result, err = LockDependencies(context.Background(), cfg, LockDependenciesRequest{})
		require.NoError(t, err)
	})
	assert.Equal(t, "empty", result.Status)
	assert.True(t, result.Incomplete, "a parent that could not be reached leaves the closure incomplete, not empty")
	assert.Len(t, result.Unreachable, 1, "the empty result names what could not be reached too")
	assert.NotEqual(t, "No remote items found", result.Message, "the message must not claim a clean, empty closure")
}

func TestLockDependencies_BuildsFromClosure(t *testing.T) {
	tmp := t.TempDir()
	registerTestRemote(t, tmp, "https://github.com/test/repo")
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)

	lf, err := remote.NewLockfileManager(tmp).Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, "https://github.com/test/repo@bundles/demo"))
	require.True(t, ok, "the pinned bundle is locked under its hashless canonical identity")
	assert.Equal(t, "abc123def456", entry.SHA)
}

// A remote bundle referenced only by an INLINE config.yaml profile (no
// directory profile) is part of sync's root set, so the post-sync lock rebuild
// must keep it too — otherwise sync installs it and lock erases it on every
// startup.
func TestLockDependencies_ProfileBundleSurvives(t *testing.T) {
	tmp := t.TempDir()
	registerTestRemote(t, tmp, "https://github.com/test/repo")
	base := testConfigWithSCMPath(tmp)
	cfg := withProfileDefs(t, base, map[string]config.Profile{
		"inline": {Bundles: []string{"https://github.com/test/repo@bundles/demo@abc123def456"}},
	})

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)

	lf, err := remote.NewLockfileManager(tmp).Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, "https://github.com/test/repo@bundles/demo"))
	require.True(t, ok, "the inline profile's bundle survives the lock rebuild")
	assert.Equal(t, "abc123def456", entry.SHA)
}

func TestLockDependencies_ConflictSurfacedImmediately(t *testing.T) {
	tmp := t.TempDir()
	registerTestRemote(t, tmp, "https://github.com/test/repo")
	writeLocalProfile(t, tmp, "a", "bundles:\n  - https://github.com/test/repo@bundles/demo@aaaaaaa\n")
	writeLocalProfile(t, tmp, "b", "bundles:\n  - https://github.com/test/repo@bundles/demo@bbbbbbb\n")
	cfg := testConfigWithSCMPath(tmp)

	// Explicit lock → hard error naming the conflict.
	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict")
	assert.Contains(t, err.Error(), "bundles/demo")

	// Startup auto-lock → warn + degrade (conflicted item dropped, here leaving none).
	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: false})
	require.NoError(t, err)
	assert.Equal(t, "empty", result.Status)
}

// testConfigWithSCMPath creates a config with the given ctxloom path for testing.
func testConfigWithSCMPath(path string) *config.Config {
	return gatedFixture(config.Fixture{
		AppPaths: []string{path},
	})
}

// A directory profile that stops parsing is UNREACHED, not removed. List used
// to skip it with only a warning, so the relock saw a complete closure without
// it and dropped every entry reached only through it. One malformed file was
// enough to erase lock state.
func TestLockDependencies_UnparseableProfileKeepsItsEntries(t *testing.T) {
	tmp := t.TempDir()
	registerTestRemote(t, tmp, "https://github.com/test/repo")
	writeLocalProfile(t, tmp, "good",
		"bundles:\n  - https://github.com/test/repo@bundles/kept@abc123def456\n")
	writeLocalProfile(t, tmp, "fragile",
		"bundles:\n  - https://github.com/test/repo@bundles/only-fragile@0123456789ab\n")
	cfg := testConfigWithSCMPath(tmp)

	first, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	require.Equal(t, 2, first.ItemCount)

	writeLocalProfile(t, tmp, "fragile", "bundles: [unterminated\n")
	var result *LockDependenciesResult
	captureStderr(t, func() {
		result, err = LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	})
	require.NoError(t, err)
	assert.True(t, result.Incomplete, "a profile that could not be read leaves the closure incomplete")

	lf, err := remote.NewLockfileManager(tmp).Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, "https://github.com/test/repo@bundles/only-fragile"))
	require.True(t, ok, "the entry reached only through the unreadable profile is preserved")
	assert.Equal(t, "0123456789ab", entry.SHA)
}

// closureRoots is shared by lock and upgrade, so an unreadable local bundle —
// here the project bundle, made unreadable by one malformed profile item —
// must surface in its unexpanded set for both to preserve what its profiles
// reached.
func TestClosureRoots_UnparseableProfileIsUnexpanded(t *testing.T) {
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "good", "bundles:\n  - go\n")
	writeLocalProfile(t, tmp, "fragile", "bundles: [unterminated\n")
	cfg := testConfigWithSCMPath(tmp)

	var unexpanded []string
	captureStderr(t, func() {
		_, unexpanded = closureRoots(cfg, cfg.GetProfileLoader())
	})
	assert.Equal(t, []string{paths.ProjectBundleName}, unexpanded)
}

// TestLockDependencies_StampsFetchedAt pins lock.yaml's per-entry fetched_at:
// a fresh entry records when it was resolved, and a relock that leaves the pin
// where it was keeps that time rather than restamping it — fetched_at answers
// "when did we pull this", which locked_at (the whole file's write time) does
// not. A zero value here is the defect: it serializes and reads as data.
func TestLockDependencies_StampsFetchedAt(t *testing.T) {
	tmp := t.TempDir()
	registerTestRemote(t, tmp, "https://github.com/test/repo")
	identity := "https://github.com/test/repo@bundles/demo"
	writeLocalProfile(t, tmp, "default", "bundles:\n  - "+identity+"@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)
	load := func() remote.LockEntry {
		t.Helper()
		lf, err := remote.NewLockfileManager(tmp).Load()
		require.NoError(t, err)
		entry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, identity))
		require.True(t, ok)
		return entry
	}

	before := time.Now().UTC()
	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	after := time.Now().UTC()

	first := load()
	require.False(t, first.FetchedAt.IsZero(), "a locked entry must record when it was fetched")
	assert.False(t, first.FetchedAt.Before(before) || first.FetchedAt.After(after),
		"fetched_at %s must fall within the lock call [%s, %s]", first.FetchedAt, before, after)

	_, err = LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	assert.True(t, first.FetchedAt.Equal(load().FetchedAt),
		"a relock that does not move the pin keeps the time it was fetched")
}

// The post-pull lock rebuild carries an existing pin whatever the manifest now
// asks for: only `deps upgrade` moves a pin, and the entry keeps recording the
// constraint its SHA was resolved from.
func TestLockDependencies_ChangedConstraintNeverMovesAnExistingPin(t *testing.T) {
	baseDir, ref, identity, c1 := setupUpgrade(t)
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)

	c2 := addFileToLocalRepo(t, srcDirOf(ref), repoV2("demo2"), "name: demo2\n")
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"@"+c2+"\n")

	_, err = LockDependencies(ctx, testConfigWithSCMPath(baseDir), LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)

	e, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, identity))
	require.True(t, ok)
	assert.Equal(t, c1, e.SHA, "a relock never moves an existing pin")
	assert.Empty(t, e.RequestedVersion, "the entry keeps the constraint its SHA was resolved from")
}
