// Lockfile operation tests verify dependency locking for reproducible installations.
// Lockfiles capture exact SHA versions of installed remote items, enabling teams to
// share consistent ctxloom configurations and enabling CI/CD reproducibility.
package operations

import (
	"context"
	"fmt"
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
// Lock generation scans installed bundles/profiles for source metadata (_source
// field), then writes a lockfile capturing exact SHAs. This enables teams to
// share consistent versions and reproduce builds exactly.

func TestLockDependencies_EmptyDirectory(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create empty bundles directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"", 0755))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	assert.Equal(t, "empty", result.Status)
	assert.Contains(t, result.Message, "No remote items")
}

func TestLockDependencies_WithSourceMetadata(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/test-remote", 0755))
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(testBaseDir)+"/test-remote", 0755))

	// Create bundle with source metadata
	bundleContent := `version: "1.0"
description: Test bundle
_source:
  sha: abc123def456
  url: https://github.com/test/repo
  version: v1
  fetched_at: "2024-01-01T00:00:00Z"
fragments:
  test:
    content: Test content
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/test-remote/my-bundle.yaml", []byte(bundleContent), 0644))

	// Create profile with source metadata
	profileContent := `_source:
  sha: def789ghi012
  url: https://github.com/test/repo
  version: v1
  fetched_at: "2024-01-02T00:00:00Z"
bundles:
  - my-bundle
`
	require.NoError(t, afero.WriteFile(fs, paths.ProfilesPath(testBaseDir)+"/test-remote/dev.yaml", []byte(profileContent), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 2, result.ItemCount)
	assert.Contains(t, result.Path, "lock.yaml")
}

func TestLockDependencies_SkipsFilesWithoutSourceMetadata(t *testing.T) {
	// Local bundles created by users shouldn't be locked - only remote installs
	// with source tracking (_source field) should appear in lockfiles.
	fs := afero.NewMemMapFs()

	// Create directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/test-remote", 0755))

	// Create bundle WITHOUT source metadata
	bundleContent := `version: "1.0"
description: Local bundle without source
fragments:
  test:
    content: Test content
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/test-remote/local-bundle.yaml", []byte(bundleContent), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	// Should return empty since no items have source metadata
	assert.Equal(t, "empty", result.Status)
	assert.Contains(t, result.Message, "No remote items")
}

func TestLockDependencies_MixedItems(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/remote1", 0755))
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/remote2", 0755))

	// Bundle with source metadata
	bundleWithMeta := `version: "1.0"
_source:
  sha: aaa111bbb222
  url: https://github.com/remote1/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/remote1/tracked.yaml", []byte(bundleWithMeta), 0644))

	// Bundle without source metadata (should be skipped)
	bundleNoMeta := `version: "1.0"
description: Local bundle
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/remote2/local.yaml", []byte(bundleNoMeta), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount) // Only the one with metadata
}

func TestLockDependencies_NestedPaths(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create nested directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/org/subdir", 0755))

	// Bundle in nested path
	bundleContent := `version: "1.0"
_source:
  sha: nested123sha
  url: https://github.com/org/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/org/deep-bundle.yaml", []byte(bundleContent), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)
}

func TestLockDependencies_InvalidYAML(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/test-remote", 0755))

	// Create bundle with invalid YAML (should be skipped)
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/test-remote/invalid.yaml", []byte("invalid: yaml: [[["), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	// Should return empty since the invalid YAML is skipped
	assert.Equal(t, "empty", result.Status)
}

func TestLockDependencies_EmptySHA(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create directory structure
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/test-remote", 0755))

	// Create bundle with empty SHA in source metadata
	bundleContent := `version: "1.0"
_source:
  sha: ""
  url: https://github.com/test/repo
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/test-remote/empty-sha.yaml", []byte(bundleContent), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	// Should return empty since SHA is empty
	assert.Equal(t, "empty", result.Status)
}

func TestLockDependencies_ProfilesOnly(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create only profiles (no bundles directory)
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(testBaseDir)+"/test-remote", 0755))

	profileContent := `_source:
  sha: profile123sha
  url: https://github.com/test/repo
bundles:
  - bundle1
`
	require.NoError(t, afero.WriteFile(fs, paths.ProfilesPath(testBaseDir)+"/test-remote/my-profile.yaml", []byte(profileContent), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FS: fs, SkipSync: true})
	require.NoError(t, err)

	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))

	// Create a profile that references a remote bundle (no slash = local, with slash = remote)
	cfg := &config.Config{
		AppPaths: []string{testBaseDir},
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{"local-only-bundle"}, // Local bundle, no sync needed
			},
		},
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

func TestLockDependencies_SkipSyncOption(t *testing.T) {
	// Verify that SkipSync: true skips the sync step
	fs := afero.NewMemMapFs()

	// Create directory structure with a bundle that has source metadata
	require.NoError(t, fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/test-remote", 0755))

	bundleContent := `_source:
  sha: abc123
  url: https://github.com/test/repo
fragments:
  test: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/test-remote/my-bundle.yaml", []byte(bundleContent), 0644))

	cfg := testConfigWithSCMPath(testBaseDir)

	// With SkipSync: true, we skip sync and go straight to lock generation
	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{
		FS:       fs,
		SkipSync: true,
	})
	require.NoError(t, err)

	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)
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
	return &remote.PullResult{LocalPath: paths.BundlesPath(testBaseDir)+"/test/item.yaml"}, nil
}

func TestInstallDependencies_EmptyLockfile(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create empty lockfile
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
			return &remote.PullResult{LocalPath: paths.BundlesPath(testBaseDir)+"/test/item.yaml"}, nil
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

	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
			return &remote.PullResult{LocalPath: paths.BundlesPath(testBaseDir)+"/test/item.yaml"}, nil
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))

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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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

	cfg := testConfigWithSCMPath(testBaseDir)

	// Note: This test will actually try to fetch from the network since we can't mock the fetcher
	// creation inside the loop. We're primarily testing that the function handles the empty case
	// and that the code reaches the registry lookup. The network calls will fail gracefully.
	result, err := CheckOutdated(context.Background(), cfg, CheckOutdatedRequest{
		FS:          fs,
		LockManager: lockManager,
		Registry:    registry,
	})

	require.NoError(t, err)
	// Result should be up_to_date since all entries failed to check (network error)
	// and thus no outdated items were found
	assert.Equal(t, "up_to_date", result.Status)
}

func TestCheckOutdated_OutdatedItems(t *testing.T) {
	// SHAs are truncated to 7 chars in output for readability - full SHAs
	// are unwieldy in terminal output while 7 chars provides sufficient uniqueness.
	fs := afero.NewMemMapFs()

	// Create lockfile with entries that have old SHAs
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))
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

func TestCheckOutdated_RegistryError(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))

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
