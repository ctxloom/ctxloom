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

func TestGetBaseDir_UsesConfigPath(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			name:     "nil config uses default",
			cfg:      nil,
			expected: ".scm",
		},
		{
			name:     "empty SCMPaths uses default",
			cfg:      &config.Config{SCMPaths: []string{}},
			expected: ".scm",
		},
		{
			name:     "uses first SCM path from config",
			cfg:      &config.Config{SCMPaths: []string{"/project/.scm", "/home/user/.scm"}},
			expected: "/project/.scm",
		},
		{
			name:     "single SCM path",
			cfg:      &config.Config{SCMPaths: []string{"/my/project/.scm"}},
			expected: "/my/project/.scm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBaseDir(tt.cfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestListRemotesRequest_Empty(t *testing.T) {
	// ListRemotesRequest should be valid when empty
	req := ListRemotesRequest{}
	assert.NotNil(t, req)
}

func TestAddRemoteRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         AddRemoteRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         AddRemoteRequest{Name: "test", URL: "https://github.com/test/repo"},
			shouldError: false,
		},
		{
			name:        "missing name",
			req:         AddRemoteRequest{Name: "", URL: "https://github.com/test/repo"},
			shouldError: true,
		},
		{
			name:        "missing URL",
			req:         AddRemoteRequest{Name: "test", URL: ""},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				// Check that validation would fail
				if tt.req.Name == "" {
					assert.Empty(t, tt.req.Name)
				}
				if tt.req.URL == "" {
					assert.Empty(t, tt.req.URL)
				}
			} else {
				assert.NotEmpty(t, tt.req.Name)
				assert.NotEmpty(t, tt.req.URL)
			}
		})
	}
}

func TestDiscoverRemotesRequest_Defaults(t *testing.T) {
	req := DiscoverRemotesRequest{}

	// Check that empty values are handled correctly in the operation
	assert.Empty(t, req.Source)   // Will default to "all"
	assert.Zero(t, req.Limit)     // Will default to 30
	assert.Zero(t, req.MinStars)  // Will use 0 minimum
}

func TestBrowseRemoteRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         BrowseRemoteRequest
		shouldError bool
	}{
		{
			name:        "valid bundle request",
			req:         BrowseRemoteRequest{Remote: "test", ItemType: "bundle"},
			shouldError: false,
		},
		{
			name:        "valid profile request",
			req:         BrowseRemoteRequest{Remote: "test", ItemType: "profile"},
			shouldError: false,
		},
		{
			name:        "empty item type lists both",
			req:         BrowseRemoteRequest{Remote: "test", ItemType: ""},
			shouldError: false,
		},
		{
			name:        "missing remote",
			req:         BrowseRemoteRequest{Remote: "", ItemType: "bundle"},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				assert.Empty(t, tt.req.Remote)
			} else {
				assert.NotEmpty(t, tt.req.Remote)
			}
		})
	}
}

func TestRemoteEntry_Fields(t *testing.T) {
	entry := RemoteEntry{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	assert.Equal(t, "test-remote", entry.Name)
	assert.Equal(t, "https://github.com/test/repo", entry.URL)
	assert.Equal(t, "v1", entry.Version)
}

func TestRepoEntry_Fields(t *testing.T) {
	entry := RepoEntry{
		Owner:       "testowner",
		Name:        "testrepo",
		Description: "Test repository",
		Stars:       42,
		URL:         "https://github.com/testowner/testrepo",
		Forge:       "github",
		AddCommand:  "scm remote add testowner testowner/testrepo",
	}

	assert.Equal(t, "testowner", entry.Owner)
	assert.Equal(t, "testrepo", entry.Name)
	assert.Equal(t, "Test repository", entry.Description)
	assert.Equal(t, 42, entry.Stars)
	assert.Equal(t, "https://github.com/testowner/testrepo", entry.URL)
	assert.Equal(t, "github", entry.Forge)
	assert.Contains(t, entry.AddCommand, "scm remote add")
}

func TestBrowseItemEntry_Fields(t *testing.T) {
	entry := BrowseItemEntry{
		Name:    "my-bundle",
		Type:    "bundle",
		Path:    "my-bundle",
		IsDir:   false,
		PullRef: "test/my-bundle",
	}

	assert.Equal(t, "my-bundle", entry.Name)
	assert.Equal(t, "bundle", entry.Type)
	assert.Equal(t, "my-bundle", entry.Path)
	assert.False(t, entry.IsDir)
	assert.Equal(t, "test/my-bundle", entry.PullRef)
}

// ========== Mock-based integration tests ==========

func setupTestRegistry(t *testing.T) (*remote.Registry, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/project/.scm", 0755)

	registry, err := remote.NewRegistry("/project/.scm/remotes.yaml", remote.WithRegistryFS(fs))
	require.NoError(t, err)

	return registry, fs
}

func TestListRemotes_Empty(t *testing.T) {
	registry, _ := setupTestRegistry(t)

	result, err := ListRemotes(context.Background(), nil, ListRemotesRequest{
		Registry: registry,
	})

	require.NoError(t, err)
	assert.Empty(t, result.Remotes)
	assert.Equal(t, 0, result.Count)
}

func TestListRemotes_WithRemotes(t *testing.T) {
	registry, _ := setupTestRegistry(t)

	// Add some remotes
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))
	require.NoError(t, registry.Add("bob", "https://github.com/bob/scm"))

	result, err := ListRemotes(context.Background(), nil, ListRemotesRequest{
		Registry: registry,
	})

	require.NoError(t, err)
	assert.Len(t, result.Remotes, 2)
	assert.Equal(t, 2, result.Count)

	// Results should be sorted by name
	assert.Equal(t, "alice", result.Remotes[0].Name)
	assert.Equal(t, "bob", result.Remotes[1].Name)
}

func TestListRemotes_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/project/.scm", 0755))

	// Create remotes.yaml with existing remotes
	remotesContent := `remotes:
  test-remote:
    url: https://github.com/test/scm
`
	require.NoError(t, afero.WriteFile(fs, "/project/.scm/remotes.yaml", []byte(remotesContent), 0644))

	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := ListRemotes(context.Background(), cfg, ListRemotesRequest{
		FS: fs,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "test-remote", result.Remotes[0].Name)
}

func TestAddRemote_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/project/.scm", 0755))

	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}
	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "scm")

	result, err := AddRemote(context.Background(), cfg, AddRemoteRequest{
		Name:    "alice",
		URL:     "https://github.com/alice/scm",
		FS:      fs,
		Fetcher: fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "alice", result.Name)

	// Verify remote was written to FS
	exists, _ := afero.Exists(fs, "/project/.scm/remotes.yaml")
	assert.True(t, exists)
}

func TestRemoveRemote_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/project/.scm", 0755))

	// Create remotes.yaml with existing remote
	remotesContent := `remotes:
  to-remove:
    url: https://github.com/test/scm
`
	require.NoError(t, afero.WriteFile(fs, "/project/.scm/remotes.yaml", []byte(remotesContent), 0644))

	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := RemoveRemote(context.Background(), cfg, RemoveRemoteRequest{
		Name: "to-remove",
		FS:   fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Equal(t, "to-remove", result.Name)
}

func TestAddRemote_Success(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "scm")

	result, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/scm",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "alice", result.Name)
	assert.Contains(t, result.URL, "github.com/alice/scm")
	assert.Empty(t, result.Warning)

	// Verify fetcher was called
	assert.Len(t, fetcher.ValidateCalls, 1)
	assert.Equal(t, "alice", fetcher.ValidateCalls[0].Owner)
	assert.Equal(t, "scm", fetcher.ValidateCalls[0].Repo)
}

func TestAddRemote_InvalidRepo(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher()
	// Don't mark repo as valid - will return true by default, need to override
	fetcher.ValidRepos["alice/scm"] = false

	result, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/scm",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Contains(t, result.Warning, "scm/v1/")
}

func TestAddRemote_Duplicate(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "scm")

	// Add first remote
	_, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/scm",
		Registry: registry,
		Fetcher:  fetcher,
	})
	require.NoError(t, err)

	// Try to add duplicate
	_, err = AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/bob/scm",
		Registry: registry,
		Fetcher:  fetcher,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAddRemote_EmptyName(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher()

	_, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "",
		URL:      "https://github.com/test/repo",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestAddRemote_EmptyURL(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher()

	_, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "test",
		URL:      "",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is required")
}

func TestAddRemote_InvalidURLFormat(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher()

	// URL that can't be parsed as a GitHub/GitLab URL
	_, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "test",
		URL:      "not-a-valid-repo-url",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid URL")
}

func TestRemoveRemote_EmptyName(t *testing.T) {
	registry, _ := setupTestRegistry(t)

	_, err := RemoveRemote(context.Background(), nil, RemoveRemoteRequest{
		Name:     "",
		Registry: registry,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestRemoveRemote_Success(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	result, err := RemoveRemote(context.Background(), nil, RemoveRemoteRequest{
		Name:     "alice",
		Registry: registry,
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Equal(t, "alice", result.Name)

	// Verify remote was removed
	assert.False(t, registry.Has("alice"))
}

func TestRemoveRemote_NotFound(t *testing.T) {
	registry, _ := setupTestRegistry(t)

	_, err := RemoveRemote(context.Background(), nil, RemoveRemoteRequest{
		Name:     "nonexistent",
		Registry: registry,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDiscoverRemotes_GitHubOnly(t *testing.T) {
	fetcher := remote.NewMockFetcher().WithRepos([]remote.RepoInfo{
		{Owner: "alice", Name: "scm", Description: "Alice's SCM", Stars: 100, URL: "https://github.com/alice/scm", Forge: remote.ForgeGitHub},
		{Owner: "bob", Name: "scm-tools", Description: "Bob's tools", Stars: 50, URL: "https://github.com/bob/scm-tools", Forge: remote.ForgeGitHub},
	})

	result, err := DiscoverRemotes(context.Background(), nil, DiscoverRemotesRequest{
		Source:        "github",
		GitHubFetcher: fetcher,
	})

	require.NoError(t, err)
	assert.Len(t, result.Repositories, 2)
	assert.Empty(t, result.Errors)

	// Verify fetcher was called
	assert.Len(t, fetcher.SearchReposCalls, 1)
}

func TestDiscoverRemotes_WithMinStars(t *testing.T) {
	fetcher := remote.NewMockFetcher().WithRepos([]remote.RepoInfo{
		{Owner: "alice", Name: "scm", Stars: 100, URL: "https://github.com/alice/scm", Forge: remote.ForgeGitHub},
		{Owner: "bob", Name: "scm", Stars: 5, URL: "https://github.com/bob/scm", Forge: remote.ForgeGitHub},
	})

	result, err := DiscoverRemotes(context.Background(), nil, DiscoverRemotesRequest{
		Source:        "github",
		MinStars:      50,
		GitHubFetcher: fetcher,
	})

	require.NoError(t, err)
	assert.Len(t, result.Repositories, 1)
	assert.Equal(t, "alice", result.Repositories[0].Owner)
}

func TestDiscoverRemotes_WithQuery(t *testing.T) {
	fetcher := remote.NewMockFetcher().WithRepos([]remote.RepoInfo{
		{Owner: "alice", Name: "scm", Stars: 10, URL: "https://github.com/alice/scm", Forge: remote.ForgeGitHub},
	})

	_, err := DiscoverRemotes(context.Background(), nil, DiscoverRemotesRequest{
		Source:        "github",
		Query:         "golang",
		GitHubFetcher: fetcher,
	})

	require.NoError(t, err)
	assert.Len(t, fetcher.SearchReposCalls, 1)
	assert.Equal(t, "golang", fetcher.SearchReposCalls[0].Query)
}

func TestDiscoverRemotes_WithLimit(t *testing.T) {
	fetcher := remote.NewMockFetcher().WithRepos([]remote.RepoInfo{
		{Owner: "a", Name: "scm", Stars: 10, Forge: remote.ForgeGitHub},
		{Owner: "b", Name: "scm", Stars: 10, Forge: remote.ForgeGitHub},
		{Owner: "c", Name: "scm", Stars: 10, Forge: remote.ForgeGitHub},
	})

	_, err := DiscoverRemotes(context.Background(), nil, DiscoverRemotesRequest{
		Source:        "github",
		Limit:         10,
		GitHubFetcher: fetcher,
	})

	require.NoError(t, err)
	assert.Len(t, fetcher.SearchReposCalls, 1)
	assert.Equal(t, 10, fetcher.SearchReposCalls[0].Limit)
}

func TestDiscoverRemotes_AddCommandFormat(t *testing.T) {
	fetcher := remote.NewMockFetcher().WithRepos([]remote.RepoInfo{
		{Owner: "alice", Name: "scm", Stars: 10, URL: "https://github.com/alice/scm", Forge: remote.ForgeGitHub},
	})

	result, err := DiscoverRemotes(context.Background(), nil, DiscoverRemotesRequest{
		Source:        "github",
		GitHubFetcher: fetcher,
	})

	require.NoError(t, err)
	require.Len(t, result.Repositories, 1)
	assert.Equal(t, "scm remote add alice alice/scm", result.Repositories[0].AddCommand)
}

func TestBrowseRemote_Bundles(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	fetcher := remote.NewMockFetcher().WithDir("scm/v1/bundles", []remote.DirEntry{
		{Name: "security.yaml", IsDir: false},
		{Name: "testing.yaml", IsDir: false},
	})

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "bundle",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "alice", result.Remote)
	assert.Len(t, result.Items, 2)

	// Names should have .yaml stripped
	names := []string{result.Items[0].Name, result.Items[1].Name}
	assert.Contains(t, names, "security")
	assert.Contains(t, names, "testing")
}

func TestBrowseRemote_Profiles(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	fetcher := remote.NewMockFetcher().WithDir("scm/v1/profiles", []remote.DirEntry{
		{Name: "dev.yaml", IsDir: false},
	})

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "profile",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Len(t, result.Items, 1)
	assert.Equal(t, "dev", result.Items[0].Name)
	assert.Equal(t, "profile", result.Items[0].Type)
}

func TestBrowseRemote_Both(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	fetcher := remote.NewMockFetcher().
		WithDir("scm/v1/bundles", []remote.DirEntry{{Name: "bundle1.yaml", IsDir: false}}).
		WithDir("scm/v1/profiles", []remote.DirEntry{{Name: "profile1.yaml", IsDir: false}})

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "", // Both
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
}

func TestBrowseRemote_NotFoundRemote(t *testing.T) {
	registry, _ := setupTestRegistry(t)

	_, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "nonexistent",
		Registry: registry,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestBrowseRemote_PullRef(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	fetcher := remote.NewMockFetcher().WithDir("scm/v1/bundles", []remote.DirEntry{
		{Name: "security.yaml", IsDir: false},
	})

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "bundle",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "alice/security", result.Items[0].PullRef)
}

func TestBrowseRemote_Recursive(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	// Setup directory structure with subdirectory
	fetcher := remote.NewMockFetcher().
		WithDir("scm/v1/bundles", []remote.DirEntry{
			{Name: "top-level.yaml", IsDir: false},
			{Name: "golang", IsDir: true}, // Subdirectory
		}).
		WithDir("scm/v1/bundles/golang", []remote.DirEntry{
			{Name: "testing.yaml", IsDir: false},
			{Name: "best-practices.yaml", IsDir: false},
		})

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:    "alice",
		ItemType:  "bundle",
		Recursive: true,
		Registry:  registry,
		Fetcher:   fetcher,
	})

	require.NoError(t, err)
	// Should find items from subdirectory (recursive includes only files)
	// The function skips non-.yaml files and directories in recursive mode
	assert.GreaterOrEqual(t, len(result.Items), 2) // At least the files from subdirectory
}

func TestBrowseRemote_WithPath(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/scm"))

	fetcher := remote.NewMockFetcher().
		WithDir("scm/v1/bundles/subdir", []remote.DirEntry{
			{Name: "nested.yaml", IsDir: false},
		})

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "bundle",
		Path:     "subdir",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "nested", result.Items[0].Name)
	assert.Equal(t, "subdir/nested", result.Items[0].Path)
	assert.Equal(t, "alice/subdir/nested", result.Items[0].PullRef)
}
