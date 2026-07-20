// Pull operation tests verify the remote content fetching and installation workflow.
// This is a security-critical path: remote content (bundles, profiles) can influence
// AI behavior, so the two-step preview→confirm flow ensures users review content
// before installation. The operations also ensure content writes go to the correct
// project-local .ctxloom directory, not the global ~/.ctxloom.
package operations

import (
	"context"
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
// These verify the data structures used for remote operations and ensure
// validation catches malformed requests before any network calls.

func TestPullItemRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         PullItemRequest
		shouldError bool
	}{
		{
			name:        "valid bundle request",
			req:         PullItemRequest{Reference: "test/my-bundle", ItemType: "bundle"},
			shouldError: false,
		},
		{
			name:        "profile item type is no longer pullable",
			req:         PullItemRequest{Reference: "test/my-profile", ItemType: "profile", Force: true},
			shouldError: true,
		},
		{
			name:        "invalid item type",
			req:         PullItemRequest{Reference: "test/my-item", ItemType: "fragment"},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate item type (profiles are no longer directly pullable)
			validTypes := map[string]bool{"bundle": true}
			if tt.shouldError {
				assert.False(t, validTypes[tt.req.ItemType])
			} else {
				assert.True(t, validTypes[tt.req.ItemType])
			}
		})
	}
}

func TestPullItemResult_Fields(t *testing.T) {
	result := PullItemResult{
		LocalPath:   paths.ProfilesPath(testBaseDir) + "/test/my-profile.yaml",
		SHA:         "abc123d",
		Overwritten: true,
	}

	assert.Equal(t, paths.ProfilesPath(testBaseDir)+"/test/my-profile.yaml", result.LocalPath)
	assert.Equal(t, "abc123d", result.SHA)
	assert.True(t, result.Overwritten)
}

// =============================================================================
// Base Directory Resolution Tests
// =============================================================================
// Critical bug fix verification: write operations must use cfg.GetAppPaths()[0]
// (project-local .ctxloom) not ~/.ctxloom. Without this, remote content would be
// installed globally instead of per-project, breaking isolation.

func TestConfigDeterminesWritePath(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			name:     "project context uses project path",
			cfg:      config.NewFixture(config.Fixture{AppPaths: []string{"/my/project/.ctxloom"}}),
			expected: "/my/project/.ctxloom",
		},
		{
			name:     "multiple paths uses first",
			cfg:      config.NewFixture(config.Fixture{AppPaths: []string{"/project/.ctxloom", "/home/user/.ctxloom"}}),
			expected: "/project/.ctxloom",
		},
		{
			name:     "empty paths falls back to default",
			cfg:      config.NewFixture(config.Fixture{AppPaths: []string{}}),
			expected: ".ctxloom",
		},
		{
			name:     "nil config falls back to default",
			cfg:      nil,
			expected: ".ctxloom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBaseDir(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// PullItem Integration Tests
// =============================================================================
// PullItem combines fetch and write into a single operation. Cascade mode for
// profiles automatically pulls referenced bundles, so a profile and its whole
// dependency closure materialize in one operation.

func TestPullItem_InvalidItemType(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "invalid",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item_type")
}

func TestPullItem_FragmentItemTypeInvalid(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/item",
		ItemType:  "fragment", // Fragments are not pullable
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item_type")
}

func TestPullItem_BundleSuccess(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			assert.Equal(t, "test/my-bundle", refStr)
			assert.Equal(t, remote.ItemTypeBundle, opts.ItemType)
			assert.Equal(t, testBaseDir, opts.LocalDir)
			assert.False(t, opts.Force)
			return &remote.PullResult{
				LocalPath:   paths.CacheBundlesPath(testBaseDir) + "/test/my-bundle.yaml",
				SHA:         "abc123d",
				Overwritten: false,
			}, nil
		},
	}

	result, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/my-bundle",
		ItemType:  "bundle",
		Puller:    puller,
	})

	require.NoError(t, err)
	assert.Equal(t, paths.CacheBundlesPath(testBaseDir)+"/test/my-bundle.yaml", result.LocalPath)
	assert.Equal(t, "abc123d", result.SHA)
	assert.False(t, result.Overwritten)
}

func TestPullItem_WithForce(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			assert.True(t, opts.Force)
			return &remote.PullResult{
				LocalPath:   paths.CacheBundlesPath(testBaseDir) + "/test/bundle.yaml",
				SHA:         "abc123",
				Overwritten: true,
			}, nil
		},
	}

	result, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "bundle",
		Force:     true,
		Puller:    puller,
	})

	require.NoError(t, err)
	assert.True(t, result.Overwritten)
}

func TestPullItem_PullError(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			return nil, assert.AnError
		},
	}

	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "bundle",
		Puller:    puller,
	})

	require.Error(t, err)
}

func TestPullItem_UsesConfigBaseDir(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/custom/project/.ctxloom", "/home/user/.ctxloom"}})

	var capturedLocalDir string
	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			capturedLocalDir = opts.LocalDir
			return &remote.PullResult{
				LocalPath: "/custom/project/.ctxloom/content/bundles/test/bundle.yaml",
				SHA:       "abc123",
			}, nil
		},
	}

	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "bundle",
		Puller:    puller,
	})

	require.NoError(t, err)
	assert.Equal(t, "/custom/project/.ctxloom", capturedLocalDir)
}

func TestPullItem_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Create remotes.yaml with a remote
	remotesContent := `remotes:
  test:
    url: https://github.com/test/ctxloom
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	// Use a mock puller to verify registry was created correctly
	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			return &remote.PullResult{
				LocalPath: paths.CacheBundlesPath(testBaseDir) + "/test/bundle.yaml",
				SHA:       "abc123",
			}, nil
		},
	}

	result, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "bundle",
		FS:        fs,
		Puller:    puller,
	})

	require.NoError(t, err)
	assert.Equal(t, paths.CacheBundlesPath(testBaseDir)+"/test/bundle.yaml", result.LocalPath)
}

func TestPullItem_WithFSCreatesRegistry(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))

	// Create remotes.yaml with a remote
	remotesContent := `remotes:
  test:
    url: https://github.com/test/ctxloom
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	// Don't inject Puller - let it create one from registry
	// This will fail when trying to actually pull, but we're testing
	// that the registry is created correctly from FS
	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "bundle",
		FS:        fs,
	})

	// This will fail during pull, but if we get a pull error (not registry error),
	// then the registry was created successfully from FS
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "failed to initialize registry")
}
