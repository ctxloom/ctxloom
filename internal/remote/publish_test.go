package remote

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPublisher is a test double for Publisher.
type mockPublisher struct {
	files           map[string]string // path -> sha
	createdFiles    map[string][]byte
	branches        []string
	pullRequests    []mockPR
	createFileErr   error
	createBranchErr error
	createPRErr     error
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
		assert.Equal(t, "ctxloom/bundles/mybundle.yaml", result.Path)
		assert.Equal(t, "newsha123", result.SHA)
		assert.True(t, result.Created)

		// Verify file was created
		assert.Contains(t, mp.createdFiles, "ctxloom/bundles/mybundle.yaml")
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
		mp.files["ctxloom/bundles/mybundle.yaml"] = "existingsha" // File already exists
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
		name     string
		expected string
	}{
		{ItemTypeBundle, "security", "ctxloom/bundles/security.yaml"},
		{ItemTypeBundle, "testing", "ctxloom/bundles/testing.yaml"},
		{ItemType(""), "unknown", "ctxloom/bundles/unknown.yaml"}, // defaults to bundles
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := buildPublishPath(tt.itemType, tt.name)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAddPublishMetadata(t *testing.T) {
	t.Run("adds metadata to valid YAML", func(t *testing.T) {
		content := []byte("description: Test bundle\n")
		result, err := addPublishMetadata(content)

		require.NoError(t, err)
		assert.Contains(t, string(result), "_published")
		assert.Contains(t, string(result), "published_at")
	})

	t.Run("does not embed the author's local path", func(t *testing.T) {
		content := []byte("description: Test bundle\n")
		result, err := addPublishMetadata(content)

		require.NoError(t, err)
		// The local filesystem path leaks the author's username/layout into
		// shared content and nothing consumes it; it must not be published.
		assert.NotContains(t, string(result), "from:")
	})

	t.Run("preserves author key order and comments", func(t *testing.T) {
		content := []byte("# leading comment\nzebra: 1 # inline\nalpha: 2\n")
		result, err := addPublishMetadata(content)

		require.NoError(t, err)
		out := string(result)
		// A map round-trip would sort alpha before zebra and drop comments; the
		// node-level edit must keep the author's order and comments intact.
		assert.Less(t, strings.Index(out, "zebra"), strings.Index(out, "alpha"),
			"author key order must be preserved")
		assert.Contains(t, out, "# leading comment")
		assert.Contains(t, out, "# inline")
		assert.Contains(t, out, "_published")
	})

	t.Run("returns invalid YAML as-is", func(t *testing.T) {
		content := []byte("invalid: yaml: [[")
		result, err := addPublishMetadata(content)

		require.NoError(t, err)
		assert.Equal(t, content, result)
	})
}

func TestNewPublisher(t *testing.T) {
	t.Run("creates GitHub publisher for GitHub URL", func(t *testing.T) {
		publisher, err := NewPublisher("https://github.com/owner/repo", AuthConfig{GitHub: "token"})
		require.NoError(t, err)
		assert.NotNil(t, publisher)
		// The publisher should be a GitHubPublisher (though we can't check the exact type)
	})

	t.Run("rejects publishing to a generic git host", func(t *testing.T) {
		_, err := NewPublisher("https://gitlab.com/owner/repo", AuthConfig{})
		require.Error(t, err)
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

	t.Run("rejects a generic git host", func(t *testing.T) {
		_, err := defaultPublisherFactory("https://gitlab.com/owner/repo", AuthConfig{})
		require.Error(t, err)
	})
}

func TestSplitTitleBody(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		body      string
		wantTitle string
		wantBody  string
	}{
		{"explicit title kept, body trimmed", "My Title", "  some body  ", "My Title", "some body"},
		{"empty both", "", "", "", ""},
		{"lift first line into title", "", "Subject line\nbody para", "Subject line", "body para"},
		{"single-line body becomes title, body emptied", "", "Just a subject", "Just a subject", ""},
		{"title set, body has newlines untouched", "T", "a\nb", "T", "a\nb"},
		{"whitespace-only inputs", "   ", "   ", "", ""},
		{"lift trims around the split", "", "  Subject  \n  rest  ", "Subject", "rest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTitle, gotBody := SplitTitleBody(tt.title, tt.body)
			assert.Equal(t, tt.wantTitle, gotTitle, "title")
			assert.Equal(t, tt.wantBody, gotBody, "body")
		})
	}
}
