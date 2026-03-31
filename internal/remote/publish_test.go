package remote

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPublisher is a test double for Publisher.
type mockPublisher struct {
	files         map[string]string // path -> sha
	createdFiles  map[string][]byte
	branches      []string
	pullRequests  []mockPR
	createFileErr error
	createBranchErr error
	createPRErr   error
}

type mockPR struct {
	title string
	body  string
	head  string
	base  string
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{
		files:        make(map[string]string),
		createdFiles: make(map[string][]byte),
		branches:     make([]string, 0),
		pullRequests: make([]mockPR, 0),
	}
}

func (m *mockPublisher) CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch, message string, content []byte) (string, error) {
	if m.createFileErr != nil {
		return "", m.createFileErr
	}
	m.createdFiles[path] = content
	sha := "newsha123"
	m.files[path] = sha
	return sha, nil
}

func (m *mockPublisher) CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) (string, error) {
	if m.createPRErr != nil {
		return "", m.createPRErr
	}
	m.pullRequests = append(m.pullRequests, mockPR{title: title, body: body, head: head, base: base})
	return "https://github.com/owner/repo/pull/1", nil
}

func (m *mockPublisher) CreateBranch(ctx context.Context, owner, repo, branchName, baseSHA string) error {
	if m.createBranchErr != nil {
		return m.createBranchErr
	}
	m.branches = append(m.branches, branchName)
	return nil
}

func (m *mockPublisher) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	if sha, ok := m.files[path]; ok {
		return sha, nil
	}
	return "", nil
}

// mockPublisherFactory creates a PublisherFactory that returns the given publisher.
func mockPublisherFactory(p Publisher) PublisherFactory {
	return func(repoURL string, auth AuthConfig) (Publisher, error) {
		return p, nil
	}
}

func TestNewPublishManager(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry, _ := NewRegistry("", WithRegistryFS(fs))

	t.Run("creates with defaults", func(t *testing.T) {
		pm := NewPublishManager(registry, AuthConfig{})
		assert.NotNil(t, pm)
		assert.NotNil(t, pm.fs)
		assert.NotNil(t, pm.publisherFactory)
		assert.NotNil(t, pm.fetcherFactory)
		assert.NotNil(t, pm.lockfileManager)
	})

	t.Run("accepts custom options", func(t *testing.T) {
		customFS := afero.NewMemMapFs()
		mp := newMockPublisher()
		mf := newMockFetcher()
		lm := NewLockfileManager("/test", WithLockfileFS(customFS))

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(customFS),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
			WithPublishLockfileManager(lm),
		)

		assert.Equal(t, customFS, pm.fs)
		assert.Equal(t, lm, pm.lockfileManager)
	})
}

func TestPublishManager_Publish(t *testing.T) {
	t.Run("publishes bundle successfully", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		// Create local bundle file
		bundleContent := "description: Test bundle\nfragments:\n  test:\n    content: hello\n"
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte(bundleContent), 0644))

		// Create registry with remote
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		// Create mock publisher and fetcher
		mp := newMockPublisher()
		mf := newMockFetcher()
		mf.defaultBranch = "main"

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType: ItemTypeBundle,
			Branch:   "main",
		})

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "ctxloom/v1/bundles/mybundle.yaml", result.Path)
		assert.Equal(t, "newsha123", result.SHA)
		assert.True(t, result.Created)

		// Verify file was created
		assert.Contains(t, mp.createdFiles, "ctxloom/v1/bundles/mybundle.yaml")
	})

	t.Run("uses remote version when specified", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.AddWithVersion("alice", "https://github.com/alice/ctxloom", "v2"))

		mp := newMockPublisher()
		mf := newMockFetcher()

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType: ItemTypeBundle,
			Branch:   "main",
		})

		require.NoError(t, err)
		assert.Equal(t, "ctxloom/v2/bundles/mybundle.yaml", result.Path)
	})

	t.Run("creates PR when requested", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mf := newMockFetcher()
		mf.refs["main"] = "basesha123"

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType: ItemTypeBundle,
			Branch:   "main",
			CreatePR: true,
		})

		require.NoError(t, err)
		assert.NotEmpty(t, result.PRURL)
		assert.Len(t, mp.branches, 1)
		assert.Len(t, mp.pullRequests, 1)
	})

	t.Run("returns error for missing remote", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		pm := NewPublishManager(registry, AuthConfig{}, WithPublishFS(fs))

		_, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "nonexistent", PublishOptions{
			ItemType: ItemTypeBundle,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "remote not found")
	})

	t.Run("returns error for missing local file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		pm := NewPublishManager(registry, AuthConfig{}, WithPublishFS(fs))

		_, err := pm.Publish(context.Background(), "/nonexistent.yaml", "alice", PublishOptions{
			ItemType: ItemTypeBundle,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read local file")
	})

	t.Run("detects update vs create", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/local", 0755))
		require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: Test\n"), 0644))

		registry, _ := NewRegistry("", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		mp := newMockPublisher()
		mp.files["ctxloom/v1/bundles/mybundle.yaml"] = "existingsha" // File already exists
		mf := newMockFetcher()

		pm := NewPublishManager(registry, AuthConfig{},
			WithPublishFS(fs),
			WithPublisherFactory(mockPublisherFactory(mp)),
			WithPublishFetcherFactory(mockFetcherFactory(mf)),
		)

		result, err := pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
			ItemType: ItemTypeBundle,
			Branch:   "main",
		})

		require.NoError(t, err)
		assert.False(t, result.Created)
	})
}

func TestBuildPublishPath(t *testing.T) {
	tests := []struct {
		itemType ItemType
		version  string
		name     string
		expected string
	}{
		{ItemTypeBundle, "v1", "security", "ctxloom/v1/bundles/security.yaml"},
		{ItemTypeBundle, "v2", "testing", "ctxloom/v2/bundles/testing.yaml"},
		{ItemTypeProfile, "v1", "development", "ctxloom/v1/profiles/development.yaml"},
		{ItemType(""), "v1", "unknown", "ctxloom/v1/bundles/unknown.yaml"}, // defaults to bundles
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := buildPublishPath(tt.itemType, tt.version, tt.name)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAddPublishMetadata(t *testing.T) {
	t.Run("adds metadata to valid YAML", func(t *testing.T) {
		content := []byte("description: Test bundle\n")
		result, err := addPublishMetadata(content, "/path/to/local.yaml")

		require.NoError(t, err)
		assert.Contains(t, string(result), "_published")
		assert.Contains(t, string(result), "/path/to/local.yaml")
	})

	t.Run("returns invalid YAML as-is", func(t *testing.T) {
		content := []byte("invalid: yaml: [[")
		result, err := addPublishMetadata(content, "/path/to/local.yaml")

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})
}

func TestTransformProfileForExport(t *testing.T) {
	t.Run("returns unchanged for canonical URLs", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/test", WithLockfileFS(fs))

		content := []byte("bundles:\n  - https://github.com/owner/repo@v1/bundles/name\n")
		result, err := transformProfileForExport(content, lm)

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})

	t.Run("returns unchanged for profiles without bundles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/test", WithLockfileFS(fs))

		content := []byte("name: test-profile\ndescription: A profile\n")
		result, err := transformProfileForExport(content, lm)

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})

	t.Run("returns invalid YAML as-is", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/test", WithLockfileFS(fs))

		content := []byte("invalid: yaml: [[")
		result, err := transformProfileForExport(content, lm)

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})

	t.Run("transforms local refs to canonical URLs", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/test", WithLockfileFS(fs))

		// Create lockfile with entry
		lockfile := &Lockfile{
			Version: 1,
			Bundles: map[string]LockEntry{
				"alice/security": {
					SHA:        "abc123",
					URL:        "https://github.com/alice/ctxloom",
					SCMVersion: "v1",
				},
			},
			Profiles: make(map[string]LockEntry),
		}
		require.NoError(t, lm.Save(lockfile))

		content := []byte("bundles:\n  - alice/security\n")
		result, err := transformProfileForExport(content, lm)

		require.NoError(t, err)
		assert.Contains(t, string(result), "https://github.com/alice/ctxloom@v1/bundles/security")
	})

	t.Run("returns error for unknown local ref", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/test", WithLockfileFS(fs))

		// Create empty lockfile
		lockfile := &Lockfile{
			Version:  1,
			Bundles:  make(map[string]LockEntry),
			Profiles: make(map[string]LockEntry),
		}
		require.NoError(t, lm.Save(lockfile))

		content := []byte("bundles:\n  - unknown/bundle\n")
		_, err := transformProfileForExport(content, lm)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in lockfile")
	})
}

func TestNewPublisher(t *testing.T) {
	t.Run("creates GitHub publisher for GitHub URL", func(t *testing.T) {
		publisher, err := NewPublisher("https://github.com/owner/repo", AuthConfig{GitHub: "token"})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
		// The publisher should be a GitHubPublisher (though we can't check the exact type)
	})

	t.Run("creates GitLab publisher for GitLab URL", func(t *testing.T) {
		publisher, err := NewPublisher("https://gitlab.com/owner/repo", AuthConfig{GitLab: "token"})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
	})

	t.Run("creates GitHub publisher for shorthand", func(t *testing.T) {
		publisher, err := NewPublisher("owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
	})
}

func TestDefaultPublisherFactory(t *testing.T) {
	t.Run("creates GitHub publisher", func(t *testing.T) {
		publisher, err := defaultPublisherFactory("https://github.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
	})

	t.Run("creates GitLab publisher", func(t *testing.T) {
		publisher, err := defaultPublisherFactory("https://gitlab.com/owner/repo", AuthConfig{})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
	})
}

func TestPublishConvenienceFunction(t *testing.T) {
	ctx := context.Background()

	t.Run("returns error when remote not found", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, _ := NewRegistry("", WithRegistryFS(fs))

		// Create a file to publish
		require.NoError(t, fs.MkdirAll("/test", 0755))
		require.NoError(t, afero.WriteFile(fs, "/test/bundle.yaml", []byte("description: test\n"), 0644))

		opts := PublishOptions{
			ItemType: ItemTypeBundle,
			FS:       fs,
		}

		_, err := Publish(ctx, "/test/bundle.yaml", "nonexistent", opts, registry, AuthConfig{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("returns error when file does not exist", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(".ctxloom", 0755))
		registry, _ := NewRegistry(".ctxloom/remotes.yaml", WithRegistryFS(fs))
		require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

		opts := PublishOptions{
			ItemType: ItemTypeBundle,
			FS:       fs,
		}

		_, err := Publish(ctx, "/nonexistent/bundle.yaml", "alice", opts, registry, AuthConfig{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read")
	})
}
