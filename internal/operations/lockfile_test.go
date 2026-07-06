// Lockfile operation tests verify dependency locking for reproducible installations.
// Lockfiles capture exact SHA versions of installed remote items, enabling teams to
// share consistent ctxloom configurations and enabling CI/CD reproducibility.
package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
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

func TestInstallDependenciesRequest_Fields(t *testing.T) {
	req := InstallDependenciesRequest{
		Force: true,
	}

	assert.True(t, req.Force)
}

func TestInstallDependenciesResult_Fields(t *testing.T) {
	result := InstallDependenciesResult{
		Status:    "completed",
		Installed: 3,
		Failed:    1,
		Total:     4,
		Errors:    []string{"test/bundle1: connection refused"},
	}

	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, 3, result.Installed)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 4, result.Total)
	assert.Len(t, result.Errors, 1)
}

func TestCheckOutdatedRequest_Empty(t *testing.T) {
	req := CheckOutdatedRequest{}
	assert.NotNil(t, req)
}

func TestOutdatedItem_Fields(t *testing.T) {
	item := OutdatedItem{
		Type:      "bundle",
		Reference: "test/my-bundle",
		LockedSHA: "abc123d",
		LatestSHA: "def456e",
	}

	assert.Equal(t, "bundle", item.Type)
	assert.Equal(t, "test/my-bundle", item.Reference)
	assert.Equal(t, "abc123d", item.LockedSHA)
	assert.Equal(t, "def456e", item.LatestSHA)
}

func TestCheckOutdatedResult_UpToDate(t *testing.T) {
	result := CheckOutdatedResult{
		Status:  "up_to_date",
		Message: "All items are up to date",
	}

	assert.Equal(t, "up_to_date", result.Status)
	assert.NotEmpty(t, result.Message)
}

func TestCheckOutdatedResult_Outdated(t *testing.T) {
	result := CheckOutdatedResult{
		Status: "outdated",
		Count:  2,
		Items: []OutdatedItem{
			{Type: "bundle", Reference: "test/bundle1", LockedSHA: "aaa", LatestSHA: "bbb"},
			{Type: "profile", Reference: "test/profile1", LockedSHA: "ccc", LatestSHA: "ddd"},
		},
		Total: 5,
	}

	assert.Equal(t, "outdated", result.Status)
	assert.Equal(t, 2, result.Count)
	assert.Len(t, result.Items, 2)
	assert.Equal(t, 5, result.Total)
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
func TestLockDependencies_InlineConfigProfileBundleSurvives(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfigWithSCMPath(tmp)
	cfg.Profiles = config.ProfilesConfig{Definitions: map[string]config.Profile{
		"inline": {Bundles: []string{"https://github.com/test/repo@bundles/demo@abc123def456"}},
	}}

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
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755))
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Create a profile that references a remote bundle (no slash = local, with slash = remote)
	cfg := &config.Config{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"local-only-bundle"}, // Local bundle, no sync needed
			},
		}},
	}

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
	return &config.Config{
		AppPaths: []string{path},
	}
}

// =============================================================================
// InstallDependencies Integration Tests
// =============================================================================
// Install restores exact versions from the lockfile, enabling reproducible
// setups across machines and CI. The Force flag allows re-downloading even
// if items exist locally (useful after corruption or when testing updates).

// mockPuller implements the Puller interface for testing.
type mockPuller struct {
	pullFunc func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

func (m *mockPuller) Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
	if m.pullFunc != nil {
		return m.pullFunc(ctx, refStr, opts)
	}
	return &remote.PullResult{LocalPath: paths.BundlesPath(testBaseDir) + "/test/item.yaml"}, nil
}

func TestInstallDependencies_EmptyLockfile(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create empty lockfile
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles: {}
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := InstallDependencies(context.Background(), cfg, InstallDependenciesRequest{
		FS:          fs,
		LockManager: lockManager,
	})

	require.NoError(t, err)
	assert.Equal(t, "empty", result.Status)
	assert.Contains(t, result.Message, "No entries")
}

func TestInstallDependencies_Success(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create lockfile with entries
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc123def456789
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Mock puller that always succeeds
	puller := &mockPuller{}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := InstallDependencies(context.Background(), cfg, InstallDependenciesRequest{
		FS:          fs,
		LockManager: lockManager,
		Registry:    registry,
		Puller:      puller,
	})

	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 0, result.Failed)
	assert.Equal(t, 1, result.Total)
}

func TestInstallDependencies_PartialFailure(t *testing.T) {
	// Partial failures shouldn't abort the entire install - teams need to know
	// which items succeeded vs failed so they can retry or debug specific items.
	fs := afero.NewMemMapFs()

	// Create lockfile with multiple entries
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/bundle1:
    sha: abc123def456789
    url: https://github.com/test/repo
  test/bundle2:
    sha: def456ghi789012
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Mock puller that fails for one item
	callCount := 0
	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			callCount++
			if callCount == 1 {
				return nil, fmt.Errorf("network error")
			}
			return &remote.PullResult{LocalPath: paths.BundlesPath(testBaseDir) + "/test/item.yaml"}, nil
		},
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := InstallDependencies(context.Background(), cfg, InstallDependenciesRequest{
		FS:          fs,
		LockManager: lockManager,
		Registry:    registry,
		Puller:      puller,
	})

	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 2, result.Total)
	assert.Len(t, result.Errors, 1)
}

func TestInstallDependencies_WithForce(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc123def456789
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Mock puller that captures Force flag
	var capturedForce bool
	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			capturedForce = opts.Force
			return &remote.PullResult{LocalPath: paths.BundlesPath(testBaseDir) + "/test/item.yaml"}, nil
		},
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := InstallDependencies(context.Background(), cfg, InstallDependenciesRequest{
		FS:          fs,
		LockManager: lockManager,
		Registry:    registry,
		Puller:      puller,
		Force:       true,
	})

	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status)
	assert.True(t, capturedForce)
}

func TestInstallDependencies_InvalidLockfile(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	// Write invalid YAML to cause parse error
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte("invalid: yaml: content: :::"), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	cfg := testConfigWithSCMPath(testBaseDir)

	_, err := InstallDependencies(context.Background(), cfg, InstallDependenciesRequest{
		FS:          fs,
		LockManager: lockManager,
	})

	require.Error(t, err)
}

func TestInstallDependencies_RegistryError(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Create valid lockfile with entries
	lockContent := `version: 1
bundles:
  test/bundle:
    sha: abc123full
    url: https://github.com/test/ctxloom
    item: bundle
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))
	// Create invalid remotes.yaml to cause registry error
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte("{{invalid yaml"), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	cfg := testConfigWithSCMPath(testBaseDir)

	_, err := InstallDependencies(context.Background(), cfg, InstallDependenciesRequest{
		FS:          fs,
		LockManager: lockManager,
		// Don't inject Registry, let it create one which will fail
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize registry")
}

// =============================================================================
// CheckOutdated Integration Tests
// =============================================================================
// Outdated checks compare locked SHAs against latest remote refs, enabling
// users to see which dependencies have updates available. This supports
// security updates and helps teams decide when to upgrade.

func TestCheckOutdated_EmptyLockfile(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create empty lockfile
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles: {}
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:          fs,
		LockManager: lockManager,
	})

	require.NoError(t, err)
	assert.Equal(t, "empty", result.Status)
	assert.Contains(t, result.Message, "No entries")
}

func TestCheckOutdated_UpToDate(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create lockfile with entries
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc123def456789
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Inject a fetcher that resolves the default branch to the SAME SHA the
	// lockfile pins, so this genuinely exercises SHA-match up-to-date detection.
	// Injecting FetcherFactory (with RepoCache left nil) also makes the test
	// hermetic: CheckOutdated's real-network/real-FS refresh pre-pass only runs
	// when RepoCache != nil || FetcherFactory == nil, so it is skipped entirely
	// here — no git clone, no touch of the OS filesystem outside the afero memfs.
	mockFetcher := remote.NewMockFetcher()
	mockFetcher.DefaultBranch = "main"
	mockFetcher.Refs = map[string]string{
		"main": "abc123def456789", // Matches the locked SHA above.
	}
	fetcherFactory := func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return mockFetcher, nil
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:             fs,
		LockManager:    lockManager,
		Registry:       registry,
		FetcherFactory: fetcherFactory,
	})

	require.NoError(t, err)
	// Locked SHA == resolved SHA, so the item is genuinely up to date.
	assert.Equal(t, "up_to_date", result.Status)
}

func TestCheckOutdated_OutdatedItems(t *testing.T) {
	// SHAs are truncated to 7 chars in output for readability - full SHAs
	// are unwieldy in terminal output while 7 chars provides sufficient uniqueness.
	fs := afero.NewMemMapFs()

	// Create lockfile with entries that have old SHAs
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc123def4567890123456789012345678901234
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Create mock fetcher that returns a different SHA (simulating outdated item)
	mockFetcher := remote.NewMockFetcher()
	mockFetcher.DefaultBranch = "main"
	mockFetcher.Refs = map[string]string{
		"main": "xyz999def4567890123456789012345678901234", // Different from locked SHA
	}

	fetcherFactory := func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return mockFetcher, nil
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:             fs,
		LockManager:    lockManager,
		Registry:       registry,
		FetcherFactory: fetcherFactory,
	})

	require.NoError(t, err)
	assert.Equal(t, "outdated", result.Status)
	assert.Equal(t, 1, result.Count)
	assert.Len(t, result.Items, 1)
	assert.Equal(t, "bundle", result.Items[0].Type)
	assert.Equal(t, "test/my-bundle", result.Items[0].Reference)
	assert.Equal(t, "abc123d", result.Items[0].LockedSHA) // Truncated to 7 chars
	assert.Equal(t, "xyz999d", result.Items[0].LatestSHA) // Truncated to 7 chars
}

func TestCheckOutdated_AllUpToDate(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create lockfile with entries that have matching SHAs
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc123def4567890123456789012345678901234
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Create mock fetcher that returns the same SHA (up to date)
	mockFetcher := remote.NewMockFetcher()
	mockFetcher.DefaultBranch = "main"
	mockFetcher.Refs = map[string]string{
		"main": "abc123def4567890123456789012345678901234", // Same as locked SHA
	}

	fetcherFactory := func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return mockFetcher, nil
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:             fs,
		LockManager:    lockManager,
		Registry:       registry,
		FetcherFactory: fetcherFactory,
	})

	require.NoError(t, err)
	assert.Equal(t, "up_to_date", result.Status)
	assert.Contains(t, result.Message, "up to date")
}

func TestCheckOutdated_InvalidReference(t *testing.T) {
	// Invalid references should be skipped rather than crashing - lockfiles may
	// be manually edited or come from older ctxloom versions with different formats.
	fs := afero.NewMemMapFs()

	// Create lockfile with entries that have an invalid reference format
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  invalid-ref-no-slash:
    sha: abc123def4567890123456789012345678901234
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	cfg := testConfigWithSCMPath(testBaseDir)

	// Should handle gracefully (continue past invalid refs)
	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:          fs,
		LockManager: lockManager,
		Registry:    registry,
	})

	require.NoError(t, err)
	// Result should be up_to_date since all entries failed parsing
	assert.Equal(t, "up_to_date", result.Status)
}

func TestCheckOutdated_FetcherError(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create lockfile with entries
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc123def4567890123456789012345678901234
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Create mock fetcher that returns errors
	fetcherFactory := func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return nil, fmt.Errorf("failed to create fetcher")
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:             fs,
		LockManager:    lockManager,
		Registry:       registry,
		FetcherFactory: fetcherFactory,
	})

	require.NoError(t, err)
	// Should handle gracefully (continue past failed fetcher creation)
	assert.Equal(t, "up_to_date", result.Status)
}

func TestCheckOutdated_ShortSHA(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create lockfile with entries that have short SHAs (less than 7 chars)
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	lockContent := `version: 1
bundles:
  test/my-bundle:
    sha: abc12
    url: https://github.com/test/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	// Create remotes.yaml
	remotesContent := `remotes:
  test:
    url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	// Create mock fetcher that returns a short SHA
	mockFetcher := remote.NewMockFetcher()
	mockFetcher.DefaultBranch = "main"
	mockFetcher.Refs = map[string]string{
		"main": "xyz99", // Short SHA (less than 7 chars)
	}

	fetcherFactory := func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return mockFetcher, nil
	}

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:             fs,
		LockManager:    lockManager,
		Registry:       registry,
		FetcherFactory: fetcherFactory,
	})

	require.NoError(t, err)
	assert.Equal(t, "outdated", result.Status)
	// Short SHAs should be kept as-is (not truncated)
	assert.Equal(t, "abc12", result.Items[0].LockedSHA)
	assert.Equal(t, "xyz99", result.Items[0].LatestSHA)
}

// TestCheckOutdated_DedupsFetcherPerURL asserts that when many lockfile
// entries share a repo URL, the injected FetcherFactory is invoked only once
// per unique URL — the cached fetcher is reused for every additional entry.
// This is what makes the rework "no mass calls": even with 50 bundles in one
// repo, we open the clone exactly once.
func TestCheckOutdated_DedupsFetcherPerURL(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Two remotes (two unique URLs), five bundles each → 10 entries total.
	lockContent := `version: 1
bundles:
  alpha/one:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/two:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/three:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/four:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/five:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  beta/one:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/two:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/three:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/four:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/five:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	remotesContent := `remotes:
  alpha:
    url: https://github.com/alpha/repo
  beta:
    url: https://github.com/beta/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	mock := remote.NewMockFetcher()
	mock.DefaultBranch = "main"
	mock.Refs = map[string]string{"main": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	factoryCalls := map[string]int{}
	fetcherFactory := func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		factoryCalls[url]++
		return mock, nil
	}

	cfg := testConfigWithSCMPath(testBaseDir)
	_, err = CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:             fs,
		LockManager:    lockManager,
		Registry:       registry,
		FetcherFactory: fetcherFactory,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, factoryCalls["https://github.com/alpha/repo"],
		"factory should be invoked exactly once per unique URL")
	assert.Equal(t, 1, factoryCalls["https://github.com/beta/repo"],
		"factory should be invoked exactly once per unique URL")
	assert.Len(t, factoryCalls, 2, "exactly two unique URLs")
}

func TestCheckOutdated_RegistryError(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Create valid lockfile with entries
	lockContent := `version: 1
bundles:
  test/bundle:
    sha: abc123full
    url: https://github.com/test/ctxloom
    item: bundle
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))
	// Create invalid remotes.yaml to cause registry error
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte("{{invalid yaml"), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	cfg := testConfigWithSCMPath(testBaseDir)

	_, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:          fs,
		LockManager: lockManager,
		// Don't inject Registry, let it create one which will fail
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize registry")
}

// TestUniqueRemoteURLs covers the dedup invariant that backs the production
// CheckOutdated path: when many lockfile entries share a repo URL, the unique
// list returned drives both `cache.UpdateRepo` (one git fetch) and fetcher
// construction (one open per repo). The existing
// TestCheckOutdated_DedupsFetcherPerURL test exercises the fetcher-loop side
// indirectly; this test pins down the shared helper directly so both call
// sites stay correct as the code evolves.
func TestUniqueRemoteURLs(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	remotesContent := `remotes:
  alpha:
    url: https://github.com/alpha/repo
  beta:
    url: https://github.com/beta/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	entries := []struct {
		Type  remote.ItemType
		Ref   string
		Entry remote.LockEntry
	}{
		{remote.ItemTypeBundle, "https://github.com/alpha/repo@bundles/one", remote.LockEntry{SHA: "a", URL: "https://github.com/alpha/repo"}},
		{remote.ItemTypeBundle, "https://github.com/alpha/repo@bundles/two", remote.LockEntry{SHA: "a", URL: "https://github.com/alpha/repo"}},
		{remote.ItemTypeBundle, "https://github.com/beta/repo@bundles/one", remote.LockEntry{SHA: "b", URL: "https://github.com/beta/repo"}},
		{remote.ItemTypeBundle, "https://github.com/alpha/repo@bundles/three", remote.LockEntry{SHA: "a", URL: "https://github.com/alpha/repo"}},
		{remote.ItemTypeBundle, "https://github.com/beta/repo@bundles/two", remote.LockEntry{SHA: "b", URL: "https://github.com/beta/repo"}},
		{remote.ItemTypeBundle, "ghost@bundles/one", remote.LockEntry{SHA: "g"}}, // no URL → skipped
	}

	urls := uniqueRemoteURLs(entries, registry)

	assert.Equal(t, []string{
		"https://github.com/alpha/repo",
		"https://github.com/beta/repo",
	}, urls, "dedup by URL, preserve first-appearance order, skip entries without a URL")
}

func TestUniqueRemoteURLs_EmptyEntries(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte("remotes: {}\n"), 0644))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	urls := uniqueRemoteURLs(nil, registry)
	assert.Empty(t, urls)
}

// countingRepoCache is a RepoUpdater that records every UpdateRepo call.
// Used to assert that CheckOutdated's per-URL refresh pre-pass dedups
// requests when many lockfile entries share a repo URL.
type countingRepoCache struct {
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingRepoCache) UpdateRepo(_ context.Context, repoURL string, _ remote.ForgeType) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[repoURL]++
	return "", nil
}

// TestCheckOutdated_InjectedCacheDedupsPerURL asserts the production code
// path that refreshes repos before iterating lockfile entries: when 10 entries
// share 2 unique URLs, UpdateRepo is invoked exactly twice. The existing
// TestCheckOutdated_DedupsFetcherPerURL injects a FetcherFactory which
// short-circuits this branch — this test exercises it directly via the
// RepoCache injection point.
func TestCheckOutdated_InjectedCacheDedupsPerURL(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	lockContent := `version: 1
bundles:
  alpha/one:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/two:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/three:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/four:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  alpha/five:
    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    url: https://github.com/alpha/repo
  beta/one:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/two:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/three:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/four:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
  beta/five:
    sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    url: https://github.com/beta/repo
profiles: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.LockPath(testBaseDir), []byte(lockContent), 0644))

	remotesContent := `remotes:
  alpha:
    url: https://github.com/alpha/repo
  beta:
    url: https://github.com/beta/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	lockManager := remote.NewLockfileManager(testBaseDir, remote.WithLockfileFS(fs))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	mock := remote.NewMockFetcher()
	mock.DefaultBranch = "main"
	mock.Refs = map[string]string{"main": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	cache := &countingRepoCache{}

	cfg := testConfigWithSCMPath(testBaseDir)
	_, err = CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:          fs,
		LockManager: lockManager,
		Registry:    registry,
		FetcherFactory: func(_ string, _ remote.AuthConfig) (remote.Fetcher, error) {
			return mock, nil
		},
		RepoCache: cache,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, cache.calls["https://github.com/alpha/repo"],
		"UpdateRepo must be called exactly once per unique URL")
	assert.Equal(t, 1, cache.calls["https://github.com/beta/repo"],
		"UpdateRepo must be called exactly once per unique URL")
	assert.Len(t, cache.calls, 2, "exactly two unique URLs refreshed")
}

// initLocalRepoWithFile creates a non-bare git repo at dir, writes filePath
// with content, commits, and returns the resulting commit SHA. Used to build
// file:// remotes for cache integration tests.
func initLocalRepoWithFile(t *testing.T, dir, filePath, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))

	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	full := filepath.Join(dir, filePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))

	_, err = wt.Add(filePath)
	require.NoError(t, err)

	sha, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return sha.String()
}

// addFileToLocalRepo writes and commits an additional file into an existing
// local repo (created by initLocalRepoWithFile), returning the new commit SHA.
func addFileToLocalRepo(t *testing.T, dir, filePath, content string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	full := filepath.Join(dir, filePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))

	_, err = wt.Add(filePath)
	require.NoError(t, err)

	sha, err := wt.Commit("add "+filePath, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return sha.String()
}

// TestCheckOutdated_RealCacheWithLocalRepos drives the production path
// end-to-end against two real git repos served over file:// URLs. With
// FetcherFactory==nil and a real RepoCache rooted at a temp dir, the test
// proves that CheckOutdated:
//
//  1. Clones each unique URL exactly once into the repo cache (no extra
//     clones for repeat entries from the same URL).
//  2. Resolves the latest SHAs via the local clone (no API traffic).
//  3. Returns "up_to_date" when the lockfile SHAs match what the local clones
//     resolve.
//
// Combined with TestCheckOutdated_InjectedCacheDedupsPerURL above (which
// asserts the dedup invariant with a counter), this covers both the
// production wiring and the underlying behavior.
func TestCheckOutdated_RealCacheWithLocalRepos(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0755))

	// Two source repos with a single commit each. The committed SHA is what
	// the local clone will resolve "main" to, so we use it as the lockfile
	// SHA — that's what makes the result come back as "up_to_date".
	srcA := filepath.Join(tmpDir, "source-a")
	shaA := initLocalRepoWithFile(t, srcA, "README.md", "alpha\n")
	urlA := "file://" + srcA

	srcB := filepath.Join(tmpDir, "source-b")
	shaB := initLocalRepoWithFile(t, srcB, "README.md", "beta\n")
	urlB := "file://" + srcB

	// Lockfile with three entries from repo A and two from repo B — five
	// total, two unique URLs. Without dedup we'd clone four extra times.
	lockContent := fmt.Sprintf(`version: 1
bundles:
  alpha/one:
    sha: %s
    url: %s
  alpha/two:
    sha: %s
    url: %s
  alpha/three:
    sha: %s
    url: %s
  beta/one:
    sha: %s
    url: %s
  beta/two:
    sha: %s
    url: %s
profiles: {}
`, shaA, urlA, shaA, urlA, shaA, urlA, shaB, urlB, shaB, urlB)
	require.NoError(t, os.WriteFile(paths.LockPath(baseDir), []byte(lockContent), 0644))

	remotesContent := fmt.Sprintf(`remotes:
  alpha:
    url: %s
  beta:
    url: %s
`, urlA, urlB)
	require.NoError(t, os.WriteFile(paths.RemotesPath(baseDir), []byte(remotesContent), 0644))

	cfg := &config.Config{AppPaths: []string{baseDir}}

	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		// No injection — exercise the full production code path:
		// real LockfileManager, real Registry, real RepoCache,
		// real cached FetcherFactory.
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Each unique URL should have a single clone directory under the cache.
	cacheRoot := paths.ReposCachePath(baseDir)
	assert.DirExists(t, cacheRoot, "repos cache should be created")

	// The cache lays out clones as <cacheRoot>/<host>/<owner>/<repo>; file://
	// URLs have empty hostname, so they sit directly under <cacheRoot>/<path>.
	// We don't pin the exact layout — just assert exactly two repo dirs exist
	// under the cache, regardless of how they're nested.
	repoDirs := countGitDirs(t, cacheRoot)
	assert.Equal(t, 2, repoDirs, "exactly one clone per unique URL")

	// Both lockfile SHAs match what the local clone resolves "main" to, so
	// every entry should be up_to_date.
	assert.Equal(t, "up_to_date", result.Status,
		"all entries match their resolved SHAs, so status should be up_to_date")
}

// countGitDirs walks root and returns the number of directories that contain
// a `.git` subdirectory or look like a bare git repo (HEAD file at top level).
func countGitDirs(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			count++
			return filepath.SkipDir
		}
		return nil
	})
	require.NoError(t, err)
	return count
}
