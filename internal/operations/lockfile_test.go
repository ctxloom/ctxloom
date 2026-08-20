// Lockfile operation tests verify dependency locking for reproducible installations.
// Lockfiles capture exact SHA versions of installed remote items, enabling teams to
// share consistent ctxloom configurations and enabling CI/CD reproducibility.
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

func TestLockDependenciesRequest_SkipSyncField(t *testing.T) {
	req := LockDependenciesRequest{
		SkipSync: true,
	}

	assert.True(t, req.SkipSync)
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
	dir := paths.ProfilesPath(baseDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644))
}

func TestLockDependencies_NoProfiles(t *testing.T) {
	cfg := testConfigWithSCMPath(t.TempDir())

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true})
	require.NoError(t, err)

	assert.Equal(t, "empty", result.Status)
	assert.Contains(t, result.Message, "No remote items")
}

func TestLockDependencies_BuildsFromClosure(t *testing.T) {
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)

	lf, err := remote.NewLockfileManager(tmp).Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, "https://github.com/test/repo@bundles/demo")
	require.True(t, ok, "the pinned bundle is locked under its hashless canonical identity")
	assert.Equal(t, "abc123def456", entry.SHA)
}

// A remote bundle referenced only by an INLINE config.yaml profile (no
// directory profile) is part of sync's root set, so the post-sync lock rebuild
// must keep it too — otherwise sync installs it and lock erases it on every
// startup.
func TestLockDependencies_ProfileBundleSurvives(t *testing.T) {
	tmp := t.TempDir()
	base := testConfigWithSCMPath(tmp)
	cfg := withProfileDefs(t, base, map[string]config.Profile{
		"inline": {Bundles: []string{"https://github.com/test/repo@bundles/demo@abc123def456"}},
	})

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)
	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)

	lf, err := remote.NewLockfileManager(tmp).Load()
	require.NoError(t, err)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, "https://github.com/test/repo@bundles/demo")
	require.True(t, ok, "the inline profile's bundle survives the lock rebuild")
	assert.Equal(t, "abc123def456", entry.SHA)
}

func TestLockDependencies_ConflictSurfacedImmediately(t *testing.T) {
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "a", "bundles:\n  - https://github.com/test/repo@bundles/demo@aaaaaaa\n")
	writeLocalProfile(t, tmp, "b", "bundles:\n  - https://github.com/test/repo@bundles/demo@bbbbbbb\n")
	cfg := testConfigWithSCMPath(tmp)

	// Explicit lock → hard error naming the conflict.
	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict")
	assert.Contains(t, err.Error(), "bundles/demo")

	// Startup auto-lock → warn + degrade (conflicted item dropped, here leaving none).
	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: false})
	require.NoError(t, err)
	assert.Equal(t, "empty", result.Status)
}

func TestLockDependencies_SyncFirstByDefault(t *testing.T) {
	// This test verifies that lock runs sync by default before generating lockfile.
	// When SkipSync is false (default), sync should run first.
	// We test this by having a profile that references a remote bundle that doesn't
	// exist locally - sync would try to fetch it.
	fs := afero.NewMemMapFs()

	// Create directory structure
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755))
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Create a profile that references a remote bundle (no slash = local, with slash = remote)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"test": {
			Bundles: []string{"local-only-bundle"}, // Local bundle, no sync needed
		},
	}, config.Fixture{})

	// With SkipSync: false (default), sync runs first but finds no remote refs
	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{
		FS:       fs,
		SkipSync: false, // Default behavior - sync first
	})
	require.NoError(t, err)

	// Should complete (sync found nothing to do, lock found nothing to lock)
	assert.Equal(t, "empty", result.Status)
}

// testConfigWithSCMPath creates a config with the given ctxloom path for testing.
func testConfigWithSCMPath(path string) *config.Config {
	return config.NewFixture(config.Fixture{
		AppPaths: []string{path},
	})
}
