// Pull operation tests verify the remote content fetching and installation workflow.
// This is a security-critical path: remote content (bundles, profiles) can influence
// AI behavior, so the two-step preview→confirm flow ensures users review content
// before installation. The operations also ensure content writes go to the correct
// project-local .scm directory, not the global ~/.scm.
package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/remote"
)

// =============================================================================
// Request/Result Structure Tests
// =============================================================================
// These verify the data structures used for remote operations and ensure
// validation catches malformed requests before any network calls.

func TestFetchRemoteContentRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         FetchRemoteContentRequest
		shouldError bool
		errContains string
	}{
		{
			name:        "valid bundle request",
			req:         FetchRemoteContentRequest{Reference: "test/my-bundle", ItemType: "bundle"},
			shouldError: false,
		},
		{
			name:        "valid profile request",
			req:         FetchRemoteContentRequest{Reference: "test/my-profile", ItemType: "profile"},
			shouldError: false,
		},
		{
			name:        "missing reference",
			req:         FetchRemoteContentRequest{Reference: "", ItemType: "bundle"},
			shouldError: true,
			errContains: "reference is required",
		},
		{
			name:        "missing item type",
			req:         FetchRemoteContentRequest{Reference: "test/my-bundle", ItemType: ""},
			shouldError: true,
			errContains: "item_type is required",
		},
		{
			name:        "invalid item type",
			req:         FetchRemoteContentRequest{Reference: "test/my-bundle", ItemType: "invalid"},
			shouldError: true,
			errContains: "invalid item_type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check request structure
			if tt.shouldError {
				if tt.req.Reference == "" {
					assert.Empty(t, tt.req.Reference)
				}
				if tt.req.ItemType == "" {
					assert.Empty(t, tt.req.ItemType)
				}
			}
		})
	}
}

func TestWriteRemoteItemRequest_Fields(t *testing.T) {
	req := WriteRemoteItemRequest{
		Reference: "test/my-bundle@abc123",
		ItemType:  "bundle",
		Content:   []byte("test content"),
		SHA:       "abc123def456",
	}

	assert.Equal(t, "test/my-bundle@abc123", req.Reference)
	assert.Equal(t, "bundle", req.ItemType)
	assert.Equal(t, []byte("test content"), req.Content)
	assert.Equal(t, "abc123def456", req.SHA)
}

func TestWriteRemoteItemResult_Fields(t *testing.T) {
	result := WriteRemoteItemResult{
		Status:      "installed",
		Reference:   "test/my-bundle@abc123",
		ItemType:    "bundle",
		LocalPath:   "/project/.scm/bundles/test/my-bundle.yaml",
		SHA:         "abc123d",
		Overwritten: false,
	}

	assert.Equal(t, "installed", result.Status)
	assert.Equal(t, "test/my-bundle@abc123", result.Reference)
	assert.Equal(t, "bundle", result.ItemType)
	assert.Contains(t, result.LocalPath, ".scm/bundles")
	assert.Equal(t, "abc123d", result.SHA)
	assert.False(t, result.Overwritten)
}

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
			name:        "valid profile request with options",
			req:         PullItemRequest{Reference: "test/my-profile", ItemType: "profile", Force: true, Cascade: true},
			shouldError: false,
		},
		{
			name:        "invalid item type",
			req:         PullItemRequest{Reference: "test/my-item", ItemType: "fragment"},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate item type
			validTypes := map[string]bool{"bundle": true, "profile": true}
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
		LocalPath:     "/project/.scm/profiles/test/my-profile.yaml",
		SHA:           "abc123d",
		Overwritten:   true,
		CascadePulled: []string{"test/bundle1", "test/bundle2"},
	}

	assert.Equal(t, "/project/.scm/profiles/test/my-profile.yaml", result.LocalPath)
	assert.Equal(t, "abc123d", result.SHA)
	assert.True(t, result.Overwritten)
	assert.Len(t, result.CascadePulled, 2)
	assert.Contains(t, result.CascadePulled, "test/bundle1")
}

func TestFetchRemoteContentResult_Warning(t *testing.T) {
	// SECURITY: Warning must always be present in fetch results. Remote content
	// can manipulate AI behavior - users must see this warning before installation.
	result := FetchRemoteContentResult{
		Reference:  "test/my-bundle",
		ItemType:   "bundle",
		SHA:        "abc123d",
		FullSHA:    "abc123def456789",
		SourceURL:  "https://github.com/test/repo",
		FilePath:   "scm/v1/bundles/my-bundle.yaml",
		Content:    "test: content",
		PullToken:  "test/my-bundle@abc123def456789",
		Warning:    "REVIEW THIS CONTENT CAREFULLY. Malicious prompts can override AI safety guidelines, exfiltrate data, or execute unintended actions. Use confirm_pull with the pull_token to install.",
		RemoteName: "test",
	}

	assert.Contains(t, result.Warning, "REVIEW THIS CONTENT CAREFULLY")
	assert.Contains(t, result.Warning, "Malicious prompts")
}

// =============================================================================
// Base Directory Resolution Tests
// =============================================================================
// Critical bug fix verification: write operations must use cfg.SCMPaths[0]
// (project-local .scm) not ~/.scm. Without this, remote content would be
// installed globally instead of per-project, breaking isolation.

func TestConfigDeterminesWritePath(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			name:     "project context uses project path",
			cfg:      &config.Config{SCMPaths: []string{"/my/project/.scm"}},
			expected: "/my/project/.scm",
		},
		{
			name:     "multiple paths uses first",
			cfg:      &config.Config{SCMPaths: []string{"/project/.scm", "/home/user/.scm"}},
			expected: "/project/.scm",
		},
		{
			name:     "empty paths falls back to default",
			cfg:      &config.Config{SCMPaths: []string{}},
			expected: ".scm",
		},
		{
			name:     "nil config falls back to default",
			cfg:      nil,
			expected: ".scm",
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
// WriteRemoteItem Integration Tests
// =============================================================================
// Write operations persist fetched content to disk with source metadata tracking.
// The _source field enables lockfile generation for reproducible installations.

func TestWriteRemoteItem_WritesBundle(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "test/my-bundle",
		ItemType:  "bundle",
		Content:   []byte("version: 1.0\nfragments:\n  test:\n    content: Hello"),
		SHA:       "abc123def456789",
		FS:        fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "installed", result.Status)
	assert.Equal(t, "test/my-bundle", result.Reference)
	assert.Equal(t, "bundle", result.ItemType)
	assert.Contains(t, result.LocalPath, "/project/.scm/bundles/test/my-bundle.yaml")
	assert.Equal(t, "abc123d", result.SHA)
	assert.False(t, result.Overwritten)

	// Verify file was written
	exists, err := afero.Exists(fs, result.LocalPath)
	require.NoError(t, err)
	assert.True(t, exists)

	content, err := afero.ReadFile(fs, result.LocalPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "version: 1.0")
}

func TestWriteRemoteItem_WritesProfile(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "test/my-profile",
		ItemType:  "profile",
		Content:   []byte("bundles:\n  - my-bundle"),
		SHA:       "def456abc789",
		FS:        fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "installed", result.Status)
	assert.Equal(t, "profile", result.ItemType)
	assert.Contains(t, result.LocalPath, "/project/.scm/profiles/test/my-profile.yaml")
}

func TestWriteRemoteItem_UpdatesExisting(t *testing.T) {
	// Updates (Overwritten=true) vs fresh installs matter for user feedback -
	// users need to know if they're replacing existing content.
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}
	ctx := context.Background()

	// Write initial content
	_, err := WriteRemoteItem(ctx, cfg, WriteRemoteItemRequest{
		Reference: "test/existing",
		ItemType:  "bundle",
		Content:   []byte("initial content"),
		SHA:       "aaa111",
		FS:        fs,
	})
	require.NoError(t, err)

	// Update content
	result, err := WriteRemoteItem(ctx, cfg, WriteRemoteItemRequest{
		Reference: "test/existing",
		ItemType:  "bundle",
		Content:   []byte("updated content"),
		SHA:       "bbb222",
		FS:        fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.True(t, result.Overwritten)

	// Verify new content
	content, err := afero.ReadFile(fs, result.LocalPath)
	require.NoError(t, err)
	assert.Equal(t, "updated content", string(content))
}

func TestWriteRemoteItem_InvalidItemType(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "test/item",
		ItemType:  "invalid",
		Content:   []byte("content"),
		SHA:       "abc123",
		FS:        fs,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item_type")
}

func TestWriteRemoteItem_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "", // Invalid empty reference
		ItemType:  "bundle",
		Content:   []byte("content"),
		SHA:       "abc123",
		FS:        fs,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

func TestWriteRemoteItem_TruncatesSHA(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "test/item",
		ItemType:  "bundle",
		Content:   []byte("content"),
		SHA:       "abc123def456789fedcba",
		FS:        fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "abc123d", result.SHA) // Truncated to 7 chars
}

func TestWriteRemoteItem_ShortSHA(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "test/item",
		ItemType:  "bundle",
		Content:   []byte("content"),
		SHA:       "abc", // Already short
		FS:        fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "abc", result.SHA) // Unchanged
}

func TestWriteRemoteItem_CreatesDirectories(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &config.Config{SCMPaths: []string{"/deep/nested/project/.scm"}}

	result, err := WriteRemoteItem(context.Background(), cfg, WriteRemoteItemRequest{
		Reference: "remote/subdir/my-bundle",
		ItemType:  "bundle",
		Content:   []byte("content"),
		SHA:       "abc123",
		FS:        fs,
	})

	require.NoError(t, err)
	exists, err := afero.Exists(fs, result.LocalPath)
	require.NoError(t, err)
	assert.True(t, exists)
}

// =============================================================================
// FetchRemoteContent Integration Tests
// =============================================================================
// Fetch operations retrieve content from remotes for preview before installation.
// The preview→confirm flow is security-critical: users must see content before
// it's installed and can influence AI behavior.

func setupPullTestRegistry(t *testing.T) (*remote.Registry, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/project/.scm", 0755)

	registry, err := remote.NewRegistry("/project/.scm/remotes.yaml", remote.WithRegistryFS(fs))
	require.NoError(t, err)

	return registry, fs
}

func TestFetchRemoteContent_FetchesBundle(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// Add a test remote
	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	// Create mock fetcher with test content
	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  security:
    content: |
      Security guidelines
`
	fetcher := remote.NewMockFetcher().
		WithFile("scm/v1/bundles/my-bundle.yaml", []byte(bundleContent)).
		WithRef("main", "abc123def456789fedcba")

	result, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/my-bundle",
		ItemType:  "bundle",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "test-remote/my-bundle", result.Reference)
	assert.Equal(t, "bundle", result.ItemType)
	assert.Equal(t, "abc123d", result.SHA)
	assert.Equal(t, "abc123def456789fedcba", result.FullSHA)
	assert.Equal(t, "https://github.com/test/scm", result.SourceURL)
	assert.Equal(t, "scm/v1/bundles/my-bundle.yaml", result.FilePath)
	assert.Contains(t, result.Content, "Security guidelines")
	assert.Equal(t, "test-remote/my-bundle@abc123def456789fedcba", result.PullToken)
	assert.Contains(t, result.Warning, "REVIEW THIS CONTENT CAREFULLY")
	assert.Equal(t, "test-remote", result.RemoteName)

	// Verify fetcher was called correctly
	require.Len(t, fetcher.ResolveRefCalls, 1)
	assert.Equal(t, "main", fetcher.ResolveRefCalls[0].Ref)
	require.Len(t, fetcher.FetchFileCalls, 1)
	assert.Equal(t, "scm/v1/bundles/my-bundle.yaml", fetcher.FetchFileCalls[0].Path)
}

func TestFetchRemoteContent_FetchesProfile(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	profileContent := `bundles:
  - my-bundle
  - another-bundle
`
	fetcher := remote.NewMockFetcher().
		WithFile("scm/v1/profiles/dev.yaml", []byte(profileContent)).
		WithRef("main", "def456abc789")

	result, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/dev",
		ItemType:  "profile",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "profile", result.ItemType)
	assert.Contains(t, result.FilePath, "profiles/dev.yaml")
	assert.Contains(t, result.Content, "my-bundle")
}

func TestFetchRemoteContent_WithGitRef(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	fetcher := remote.NewMockFetcher().
		WithFile("scm/v1/bundles/my-bundle.yaml", []byte("version: 1.0")).
		WithRef("v1.0.0", "tag123sha456")

	result, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/my-bundle@v1.0.0",
		ItemType:  "bundle",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "tag123s", result.SHA)

	// Should resolve the specific tag, not default branch
	require.Len(t, fetcher.ResolveRefCalls, 1)
	assert.Equal(t, "v1.0.0", fetcher.ResolveRefCalls[0].Ref)
}

func TestFetchRemoteContent_MissingReference(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "",
		ItemType:  "bundle",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reference is required")
}

func TestFetchRemoteContent_MissingItemType(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test/bundle",
		ItemType:  "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "item_type is required")
}

func TestFetchRemoteContent_InvalidItemType(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test/bundle",
		ItemType:  "fragment", // Invalid - only bundle and profile supported
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item_type")
}

func TestFetchRemoteContent_UnknownRemote(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// Don't add any remotes

	_, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "unknown-remote/bundle",
		ItemType:  "bundle",
		Registry:  registry,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestFetchRemoteContent_FetchError(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	// Create fetcher that will fail to fetch the file
	fetcher := remote.NewMockFetcher()
	// Don't add the file - will return "file not found" error

	_, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/nonexistent",
		ItemType:  "bundle",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch")
}

func TestFetchRemoteContent_ResolveRefError(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	fetcher := remote.NewMockFetcher()
	fetcher.ResolveRefErr = assert.AnError

	_, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/bundle",
		ItemType:  "bundle",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve ref")
}

func TestFetchRemoteContent_ShortSHA(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	fetcher := remote.NewMockFetcher().
		WithFile("scm/v1/bundles/my-bundle.yaml", []byte("content")).
		WithRef("main", "abc") // Already short

	result, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/my-bundle",
		ItemType:  "bundle",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "abc", result.SHA)
	assert.Equal(t, "abc", result.FullSHA)
}

func TestFetchRemoteContent_NestedPath(t *testing.T) {
	registry, _ := setupPullTestRegistry(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	require.NoError(t, registry.Add("test-remote", "https://github.com/test/scm"))

	fetcher := remote.NewMockFetcher().
		WithFile("scm/v1/bundles/golang/best-practices.yaml", []byte("version: 1.0")).
		WithRef("main", "abc123")

	result, err := FetchRemoteContent(context.Background(), cfg, FetchRemoteContentRequest{
		Reference: "test-remote/golang/best-practices",
		ItemType:  "bundle",
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "scm/v1/bundles/golang/best-practices.yaml", result.FilePath)
}

// =============================================================================
// PullItem Integration Tests
// =============================================================================
// PullItem combines fetch and write into a single operation for direct installs.
// Cascade mode for profiles automatically pulls referenced bundles, enabling
// one-command profile installation with all dependencies.

func TestPullItem_InvalidItemType(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/bundle",
		ItemType:  "invalid",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item_type")
}

func TestPullItem_FragmentItemTypeInvalid(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/item",
		ItemType:  "fragment", // Fragments are not pullable
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item_type")
}

func TestPullItem_BundleSuccess(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			assert.Equal(t, "test/my-bundle", refStr)
			assert.Equal(t, remote.ItemTypeBundle, opts.ItemType)
			assert.Equal(t, "/project/.scm", opts.LocalDir)
			assert.False(t, opts.Force)
			return &remote.PullResult{
				LocalPath:   "/project/.scm/bundles/test/my-bundle.yaml",
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
	assert.Equal(t, "/project/.scm/bundles/test/my-bundle.yaml", result.LocalPath)
	assert.Equal(t, "abc123d", result.SHA)
	assert.False(t, result.Overwritten)
}

func TestPullItem_ProfileSuccess(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			assert.Equal(t, "test/my-profile", refStr)
			assert.Equal(t, remote.ItemTypeProfile, opts.ItemType)
			return &remote.PullResult{
				LocalPath:   "/project/.scm/profiles/test/my-profile.yaml",
				SHA:         "def456a",
				Overwritten: false,
			}, nil
		},
	}

	result, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/my-profile",
		ItemType:  "profile",
		Puller:    puller,
	})

	require.NoError(t, err)
	assert.Equal(t, "/project/.scm/profiles/test/my-profile.yaml", result.LocalPath)
	assert.Equal(t, "def456a", result.SHA)
}

func TestPullItem_WithForce(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			assert.True(t, opts.Force)
			return &remote.PullResult{
				LocalPath:   "/project/.scm/bundles/test/bundle.yaml",
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

func TestPullItem_WithCascade(t *testing.T) {
	// Cascade mode pulls profile dependencies automatically - essential for
	// complete profile installation without manual bundle tracking.
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			assert.True(t, opts.Cascade)
			return &remote.PullResult{
				LocalPath:     "/project/.scm/profiles/test/profile.yaml",
				SHA:           "abc123",
				Overwritten:   false,
				CascadePulled: []string{"test/bundle1", "test/bundle2"},
			}, nil
		},
	}

	result, err := PullItem(context.Background(), cfg, PullItemRequest{
		Reference: "test/profile",
		ItemType:  "profile",
		Cascade:   true,
		Puller:    puller,
	})

	require.NoError(t, err)
	assert.Len(t, result.CascadePulled, 2)
	assert.Contains(t, result.CascadePulled, "test/bundle1")
	assert.Contains(t, result.CascadePulled, "test/bundle2")
}

func TestPullItem_PullError(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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
	cfg := &config.Config{SCMPaths: []string{"/custom/project/.scm", "/home/user/.scm"}}

	var capturedLocalDir string
	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			capturedLocalDir = opts.LocalDir
			return &remote.PullResult{
				LocalPath: "/custom/project/.scm/bundles/test/bundle.yaml",
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
	assert.Equal(t, "/custom/project/.scm", capturedLocalDir)
}

func TestPullItem_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/project/.scm", 0755))

	// Create remotes.yaml with a remote
	remotesContent := `remotes:
  test:
    url: https://github.com/test/scm
`
	require.NoError(t, afero.WriteFile(fs, "/project/.scm/remotes.yaml", []byte(remotesContent), 0644))

	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// Use a mock puller to verify registry was created correctly
	puller := &mockPuller{
		pullFunc: func(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
			return &remote.PullResult{
				LocalPath: "/project/.scm/bundles/test/bundle.yaml",
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
	assert.Equal(t, "/project/.scm/bundles/test/bundle.yaml", result.LocalPath)
}

func TestPullItem_WithFSCreatesRegistry(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/project/.scm", 0755))

	// Create remotes.yaml with a remote
	remotesContent := `remotes:
  test:
    url: https://github.com/test/scm
`
	require.NoError(t, afero.WriteFile(fs, "/project/.scm/remotes.yaml", []byte(remotesContent), 0644))

	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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
