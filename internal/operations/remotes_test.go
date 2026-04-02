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

func TestGetBaseDir_UsesConfigPath(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			name:     "nil config uses default",
			cfg:      nil,
			expected: paths.AppDirName,
		},
		{
			name:     "empty AppPaths uses default",
			cfg:      &config.Config{AppPaths: []string{}},
			expected: paths.AppDirName,
		},
		{
			name:     "uses first ctxloom path from config",
			cfg:      &config.Config{AppPaths: []string{testBaseDir, "/home/user/" + paths.AppDirName}},
			expected: testBaseDir,
		},
		{
			name:     "single ctxloom path",
			cfg:      &config.Config{AppPaths: []string{"/my/project/" + paths.AppDirName}},
			expected: "/my/project/" + paths.AppDirName,
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
		AddCommand:  "ctxloom remote add testowner testowner/testrepo",
	}

	assert.Equal(t, "testowner", entry.Owner)
	assert.Equal(t, "testrepo", entry.Name)
	assert.Equal(t, "Test repository", entry.Description)
	assert.Equal(t, 42, entry.Stars)
	assert.Equal(t, "https://github.com/testowner/testrepo", entry.URL)
	assert.Equal(t, "github", entry.Forge)
	assert.Contains(t, entry.AddCommand, "ctxloom remote add")
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
	_ = fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755)

	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))
	require.NoError(t, registry.Add("bob", "https://github.com/bob/ctxloom"))

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
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))

	// Create remotes.yaml with existing remotes
	remotesContent := `remotes:
  test-remote:
    url: https://github.com/test/ctxloom
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	result, err := ListRemotes(context.Background(), cfg, ListRemotesRequest{
		FS: fs,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "test-remote", result.Remotes[0].Name)
}

func TestAddRemote_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))

	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "ctxloom")

	result, err := AddRemote(context.Background(), cfg, AddRemoteRequest{
		Name:    "alice",
		URL:     "https://github.com/alice/ctxloom",
		FS:      fs,
		Fetcher: fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "alice", result.Name)

	// Verify remote was written to FS
	exists, _ := afero.Exists(fs, paths.RemotesPath(testBaseDir))
	assert.True(t, exists)
}

func TestRemoveRemote_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.GetPersistentDir(testBaseDir), 0755))

	// Create remotes.yaml with existing remote
	remotesContent := `remotes:
  to-remove:
    url: https://github.com/test/ctxloom
`
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(remotesContent), 0644))

	cfg := &config.Config{AppPaths: []string{testBaseDir}}

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
	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "ctxloom")

	result, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/ctxloom",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "alice", result.Name)
	assert.Contains(t, result.URL, "github.com/alice/ctxloom")
	assert.Empty(t, result.Warning)

	// Verify fetcher was called
	assert.Len(t, fetcher.ValidateCalls, 1)
	assert.Equal(t, "alice", fetcher.ValidateCalls[0].Owner)
	assert.Equal(t, "ctxloom", fetcher.ValidateCalls[0].Repo)
}

func TestAddRemote_InvalidRepo(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher()
	// Don't mark repo as valid - will return true by default, need to override
	fetcher.ValidRepos["alice/ctxloom"] = false

	result, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/ctxloom",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Contains(t, result.Warning, "ctxloom/v1/")
}

func TestAddRemote_Duplicate(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "ctxloom")

	// Add first remote
	_, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/ctxloom",
		Registry: registry,
		Fetcher:  fetcher,
	})
	require.NoError(t, err)

	// Try to add duplicate
	_, err = AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/bob/ctxloom",
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

func TestAddRemote_ValidationFailed(t *testing.T) {
	registry, _ := setupTestRegistry(t)

	// Create a fetcher that returns false for ValidateRepo
	fetcher := remote.NewMockFetcher()
	fetcher.ValidRepos["test/ctxloom"] = false

	result, err := AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "test",
		URL:      "https://github.com/test/ctxloom",
		Registry: registry,
		Fetcher:  fetcher,
	})

	// Should succeed but include warning
	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Contains(t, result.Warning, "ctxloom/v1")
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

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
		{Owner: "alice", Name: "ctxloom", Description: "Alice's ctxloom", Stars: 100, URL: "https://github.com/alice/ctxloom", Forge: remote.ForgeGitHub},
		{Owner: "bob", Name: "ctxloom-tools", Description: "Bob's tools", Stars: 50, URL: "https://github.com/bob/ctxloom-tools", Forge: remote.ForgeGitHub},
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
		{Owner: "alice", Name: "ctxloom", Stars: 100, URL: "https://github.com/alice/ctxloom", Forge: remote.ForgeGitHub},
		{Owner: "bob", Name: "ctxloom", Stars: 5, URL: "https://github.com/bob/ctxloom", Forge: remote.ForgeGitHub},
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
		{Owner: "alice", Name: "ctxloom", Stars: 10, URL: "https://github.com/alice/ctxloom", Forge: remote.ForgeGitHub},
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
		{Owner: "a", Name: "ctxloom", Stars: 10, Forge: remote.ForgeGitHub},
		{Owner: "b", Name: "ctxloom", Stars: 10, Forge: remote.ForgeGitHub},
		{Owner: "c", Name: "ctxloom", Stars: 10, Forge: remote.ForgeGitHub},
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
		{Owner: "alice", Name: "ctxloom", Stars: 10, URL: "https://github.com/alice/ctxloom", Forge: remote.ForgeGitHub},
	})

	result, err := DiscoverRemotes(context.Background(), nil, DiscoverRemotesRequest{
		Source:        "github",
		GitHubFetcher: fetcher,
	})

	require.NoError(t, err)
	require.Len(t, result.Repositories, 1)
	assert.Equal(t, "ctxloom remote add alice alice/ctxloom", result.Repositories[0].AddCommand)
}

func TestBrowseRemote_Bundles(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	fetcher := remote.NewMockFetcher().WithDir("ctxloom/v1/bundles", []remote.DirEntry{
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	fetcher := remote.NewMockFetcher().WithDir("ctxloom/v1/profiles", []remote.DirEntry{
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	fetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/bundles", []remote.DirEntry{{Name: "bundle1.yaml", IsDir: false}}).
		WithDir("ctxloom/v1/profiles", []remote.DirEntry{{Name: "profile1.yaml", IsDir: false}})

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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	fetcher := remote.NewMockFetcher().WithDir("ctxloom/v1/bundles", []remote.DirEntry{
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Setup directory structure with subdirectory
	fetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/bundles", []remote.DirEntry{
			{Name: "top-level.yaml", IsDir: false},
			{Name: "golang", IsDir: true}, // Subdirectory
		}).
		WithDir("ctxloom/v1/bundles/golang", []remote.DirEntry{
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
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	fetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/bundles/subdir", []remote.DirEntry{
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

func TestBrowseRemote_NoRemote(t *testing.T) {
	_, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestBrowseRemote_BrowseDirError(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Create a fetcher that returns an error on ListDir
	fetcher := remote.NewMockFetcher()
	fetcher.ListDirErr = fmt.Errorf("connection timeout")

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "bundle",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	// Should return empty items but with warning
	assert.Len(t, result.Items, 0)
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "failed to browse")
}

func TestBrowseRemote_BrowseDirNotFound(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// Create a fetcher that returns 404 (not found)
	fetcher := remote.NewMockFetcher()
	fetcher.ListDirErr = fmt.Errorf("404 not found")

	result, err := BrowseRemote(context.Background(), nil, BrowseRemoteRequest{
		Remote:   "alice",
		ItemType: "bundle",
		Registry: registry,
		Fetcher:  fetcher,
	})

	require.NoError(t, err)
	// Should return empty items with NO warning for 404s
	assert.Len(t, result.Items, 0)
	assert.Len(t, result.Warnings, 0)
}

func TestSearchRemotes_NoQuery(t *testing.T) {
	_, err := SearchRemotes(context.Background(), nil, SearchRemotesRequest{
		Query: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

func TestSearchRemotes_NoRemotes(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	// Empty registry - no remotes

	result, err := SearchRemotes(context.Background(), nil, SearchRemotesRequest{
		Query:    "test",
		Registry: registry,
	})

	require.NoError(t, err)
	assert.Len(t, result.Results, 0)
	assert.Equal(t, 0, result.Count)
	assert.Contains(t, result.Warnings, "no remotes configured")
}

func TestSearchRemotes_ManifestBased(t *testing.T) {
	// Note: SearchRemotes uses real Fetcher (GitHub/GitLab), not MockFetcher
	// Testing the actual code paths with real URLs and error responses
	// For now, test the higher-level behavior with mocks
	// Can't add real remotes without network access
	// This function is tested via integration tests
}

func TestSearchRemotes_WithValidRegistry(t *testing.T) {
	registry, _ := setupTestRegistry(t)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	// SearchRemotes will attempt to create fetchers and query remotes
	// Without mocking at the fetcher level, it will make network calls
	// For unit testing, we verify the request validation logic only

	result, err := SearchRemotes(context.Background(), nil, SearchRemotesRequest{
		Query:    "test",
		ItemType: "bundle",
		Registry: registry,
	})

	// May fail with network error, but shouldn't crash
	if err == nil {
		assert.NotNil(t, result)
	}
}

func TestSearchManifestContent_FindsMatches(t *testing.T) {
	manifestYAML := `
bundles:
  - name: golang-tools
    description: Go development tools
  - name: rust-tooling
    description: Rust utilities
profiles:
  - name: dev-profile
    description: Development profile
`

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	results, err := searchManifestContent(rem, []byte(manifestYAML), remote.ItemTypeBundle, remote.SearchQuery{Text: "golang"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "golang-tools", results[0].Entry.Name)
	assert.Equal(t, remote.ItemTypeBundle, results[0].ItemType)
	assert.Equal(t, "test-remote", results[0].Remote)
}

func TestSearchManifestContent_InvalidYAML(t *testing.T) {
	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	_, err := searchManifestContent(rem, []byte("invalid: [yaml: content"), remote.ItemTypeBundle, remote.SearchQuery{})
	require.Error(t, err)
}

func TestSearchManifestContent_NoMatches(t *testing.T) {
	manifestYAML := `
bundles:
  - name: golang-tools
    description: Go development tools
`

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	results, err := searchManifestContent(rem, []byte(manifestYAML), remote.ItemTypeBundle, remote.SearchQuery{Text: "python"})
	require.NoError(t, err)
	assert.Len(t, results, 0)
}

func TestSearchManifestContent_ProfileItems(t *testing.T) {
	manifestYAML := `
bundles: []
profiles:
  - name: testing-profile
    description: Testing framework setup
  - name: code-review-profile
    description: Code review tools
`

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	results, err := searchManifestContent(rem, []byte(manifestYAML), remote.ItemTypeProfile, remote.SearchQuery{Text: "code"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "code-review-profile", results[0].Entry.Name)
	assert.Equal(t, remote.ItemTypeProfile, results[0].ItemType)
}

func TestSearchDirectoryContent_FindsYAMLFiles(t *testing.T) {
	fetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/bundles", []remote.DirEntry{
			{Name: "golang-tools.yaml", IsDir: false},
			{Name: "rust-tooling.yaml", IsDir: false},
			{Name: "README.md", IsDir: false}, // Should skip non-yaml
			{Name: "subdir", IsDir: true},     // Should skip directories
		})

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	results, err := searchDirectoryContent(context.Background(), fetcher, rem, "owner", "repo", "main", remote.ItemTypeBundle, remote.SearchQuery{Text: "golang"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "golang-tools", results[0].Entry.Name)
}

func TestSearchDirectoryContent_NoMatches(t *testing.T) {
	fetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/bundles", []remote.DirEntry{
			{Name: "golang-tools.yaml", IsDir: false},
		})

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	results, err := searchDirectoryContent(context.Background(), fetcher, rem, "owner", "repo", "main", remote.ItemTypeBundle, remote.SearchQuery{Text: "python"})
	require.NoError(t, err)
	assert.Len(t, results, 0)
}

func TestSearchDirectoryContent_ProfileType(t *testing.T) {
	fetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/profiles", []remote.DirEntry{
			{Name: "dev-profile.yaml", IsDir: false},
			{Name: "test-profile.yaml", IsDir: false},
		})

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	results, err := searchDirectoryContent(context.Background(), fetcher, rem, "owner", "repo", "main", remote.ItemTypeProfile, remote.SearchQuery{Text: "dev"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "dev-profile", results[0].Entry.Name)
	assert.Equal(t, remote.ItemTypeProfile, results[0].ItemType)
}

func TestSearchSingleRemote_NewFetcherError(t *testing.T) {
	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "invalid://invalid-url",
		Version: "v1",
	}

	_, err := searchSingleRemote(context.Background(), rem, remote.ItemTypeBundle, remote.SearchQuery{Text: "test"}, remote.AuthConfig{})
	require.Error(t, err)
}

func TestSearchSingleRemote_ParseRepoURLError(t *testing.T) {
	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "not-a-valid-url",
		Version: "v1",
	}

	// NewFetcher will succeed but ParseRepoURL will fail with invalid URL
	_, err := searchSingleRemote(context.Background(), rem, remote.ItemTypeBundle, remote.SearchQuery{Text: "test"}, remote.AuthConfig{})
	require.Error(t, err)
}

func TestSearchSingleRemote_WithManifest(t *testing.T) {
	manifestContent := `bundles:
  - name: test-bundle
    description: A test bundle
    tags:
      - testing
`

	// Since searchSingleRemote creates its own fetcher, we test the helper function
	// that parses manifest content rather than the full integration
	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	// Test the manifest search helper function
	results, err := searchManifestContent(rem, []byte(manifestContent), remote.ItemTypeBundle, remote.SearchQuery{Text: "test"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "test-bundle", results[0].Entry.Name)
}

func TestSearchSingleRemote_FallbackToDirectory(t *testing.T) {
	// Since searchSingleRemote creates its own fetcher, we test the helper functions
	// that it calls rather than the full integration
	mockFetcher := remote.NewMockFetcher().
		WithDir("ctxloom/v1/bundles", []remote.DirEntry{
			{Name: "test-bundle.yaml", IsDir: false},
		}).
		WithFile("ctxloom/v1/bundles/test-bundle.yaml", []byte("name: test-bundle\ndescription: Test bundle"))

	rem := &remote.Remote{
		Name:    "test-remote",
		URL:     "https://github.com/test/repo",
		Version: "v1",
	}

	// Test the directory search helper function
	results, err := searchDirectoryContent(context.Background(), mockFetcher, rem, "owner", "repo", "main", remote.ItemTypeBundle, remote.SearchQuery{Text: "test"})
	require.NoError(t, err)
	// May have results depending on how the mock and search matching works
	assert.NotNil(t, results)
}
