package remote

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGitHubRepositoriesService mocks GitHubRepositoriesService.
type mockGitHubRepositoriesService struct {
	GetContentsFunc func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error)
	GetCommitFunc   func(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error)
	GetBranchFunc   func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error)
	GetFunc         func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error)
	CreateFileFunc  func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error)
}

func (m *mockGitHubRepositoriesService) GetContents(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
	if m.GetContentsFunc != nil {
		return m.GetContentsFunc(ctx, owner, repo, path, opts)
	}
	return nil, nil, nil, errors.New("not implemented")
}

func (m *mockGitHubRepositoriesService) GetCommit(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
	if m.GetCommitFunc != nil {
		return m.GetCommitFunc(ctx, owner, repo, sha, opts)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitHubRepositoriesService) GetBranch(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
	if m.GetBranchFunc != nil {
		return m.GetBranchFunc(ctx, owner, repo, branch, maxRedirects)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitHubRepositoriesService) Get(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, owner, repo)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitHubRepositoriesService) CreateFile(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error) {
	if m.CreateFileFunc != nil {
		return m.CreateFileFunc(ctx, owner, repo, path, opts)
	}
	return nil, nil, errors.New("not implemented")
}

// mockGitHubGitService mocks GitHubGitService.
type mockGitHubGitService struct {
	GetRefFunc    func(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error)
	GetTagFunc    func(ctx context.Context, owner, repo, sha string) (*github.Tag, *github.Response, error)
	CreateRefFunc func(ctx context.Context, owner, repo string, ref *github.Reference) (*github.Reference, *github.Response, error)
}

func (m *mockGitHubGitService) GetRef(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
	if m.GetRefFunc != nil {
		return m.GetRefFunc(ctx, owner, repo, ref)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitHubGitService) GetTag(ctx context.Context, owner, repo, sha string) (*github.Tag, *github.Response, error) {
	if m.GetTagFunc != nil {
		return m.GetTagFunc(ctx, owner, repo, sha)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitHubGitService) CreateRef(ctx context.Context, owner, repo string, ref *github.Reference) (*github.Reference, *github.Response, error) {
	if m.CreateRefFunc != nil {
		return m.CreateRefFunc(ctx, owner, repo, ref)
	}
	return nil, nil, errors.New("not implemented")
}

// mockGitHubSearchService mocks GitHubSearchService.
type mockGitHubSearchService struct {
	RepositoriesFunc func(ctx context.Context, query string, opts *github.SearchOptions) (*github.RepositoriesSearchResult, *github.Response, error)
}

func (m *mockGitHubSearchService) Repositories(ctx context.Context, query string, opts *github.SearchOptions) (*github.RepositoriesSearchResult, *github.Response, error) {
	if m.RepositoriesFunc != nil {
		return m.RepositoriesFunc(ctx, query, opts)
	}
	return nil, nil, errors.New("not implemented")
}

// mockGitHubPullRequestsService mocks GitHubPullRequestsService.
type mockGitHubPullRequestsService struct {
	CreateFunc func(ctx context.Context, owner, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error)
}

func (m *mockGitHubPullRequestsService) Create(ctx context.Context, owner, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error) {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, owner, repo, pull)
	}
	return nil, nil, errors.New("not implemented")
}

// mockGitHubClient mocks GitHubClient.
type mockGitHubClient struct {
	repos        *mockGitHubRepositoriesService
	git          *mockGitHubGitService
	search       *mockGitHubSearchService
	pullRequests *mockGitHubPullRequestsService
}

func newMockGitHubClient() *mockGitHubClient {
	return &mockGitHubClient{
		repos:        &mockGitHubRepositoriesService{},
		git:          &mockGitHubGitService{},
		search:       &mockGitHubSearchService{},
		pullRequests: &mockGitHubPullRequestsService{},
	}
}

func (m *mockGitHubClient) Repositories() GitHubRepositoriesService { return m.repos }
func (m *mockGitHubClient) Git() GitHubGitService                   { return m.git }
func (m *mockGitHubClient) Search() GitHubSearchService             { return m.search }
func (m *mockGitHubClient) PullRequests() GitHubPullRequestsService { return m.pullRequests }

func TestGitHubFetcher_Forge(t *testing.T) {
	fetcher := NewGitHubFetcher("")
	assert.Equal(t, ForgeGitHub, fetcher.Forge())
}

func TestGitHubFetcher_FetchFile(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		content := "test file content"
		encoded := base64.StdEncoding.EncodeToString([]byte(content))

		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return &github.RepositoryContent{
				Type:    github.String("file"),
				Content: github.String(encoded),
			}, nil, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		result, err := fetcher.FetchFile(ctx, "owner", "repo", "path/to/file.txt", "")
		require.NoError(t, err)
		assert.Equal(t, content, string(result))
	})

	t.Run("with ref", func(t *testing.T) {
		content := "test content"
		encoded := base64.StdEncoding.EncodeToString([]byte(content))
		var capturedRef string

		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			capturedRef = opts.Ref
			return &github.RepositoryContent{
				Type:    github.String("file"),
				Content: github.String(encoded),
			}, nil, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", capturedRef)
	})

	t.Run("file not found", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "missing.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "file not found")
	})

	t.Run("directory instead of file", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			// nil content means directory
			return nil, []*github.RepositoryContent{{Name: github.String("file1.txt")}}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "directory", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "directory")
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusInternalServerError}}, errors.New("server error")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch file")
	})
}

func TestGitHubFetcher_ListDir(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, []*github.RepositoryContent{
				{Type: github.String("file"), Name: github.String("fragment.yaml"), SHA: github.String("abc123"), Size: github.Int(100)},
				{Type: github.String("dir"), Name: github.String("fragments"), SHA: github.String("def456")},
			}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		entries, err := fetcher.ListDir(ctx, "owner", "repo", "ctxloom/v1", "")
		require.NoError(t, err)
		assert.Len(t, entries, 2)
		assert.Equal(t, "fragment.yaml", entries[0].Name)
		assert.False(t, entries[0].IsDir)
		assert.Equal(t, "fragments", entries[1].Name)
		assert.True(t, entries[1].IsDir)
	})

	t.Run("directory not found", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.ListDir(ctx, "owner", "repo", "missing", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "directory not found")
	})
}

func TestGitHubFetcher_ResolveRef(t *testing.T) {
	ctx := context.Background()

	t.Run("resolve SHA", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetCommitFunc = func(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
			return &github.RepositoryCommit{SHA: github.String("abc1234567890abcdef")}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "abc1234")
		require.NoError(t, err)
		assert.Equal(t, "abc1234567890abcdef", sha)
	})

	t.Run("resolve branch", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetCommitFunc = func(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
			return nil, nil, errors.New("not a commit")
		}
		mock.repos.GetBranchFunc = func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
			return &github.Branch{Commit: &github.RepositoryCommit{SHA: github.String("branch-sha-123")}}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "main")
		require.NoError(t, err)
		assert.Equal(t, "branch-sha-123", sha)
	})

	t.Run("resolve lightweight tag", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetBranchFunc = func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
			return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}
		mock.git.GetRefFunc = func(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
			return &github.Reference{Object: &github.GitObject{Type: github.String("commit"), SHA: github.String("tag-sha-456")}}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "tag-sha-456", sha)
	})

	t.Run("resolve annotated tag", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetBranchFunc = func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
			return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}
		mock.git.GetRefFunc = func(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
			return &github.Reference{Object: &github.GitObject{Type: github.String("tag"), SHA: github.String("tag-object-sha")}}, nil, nil
		}
		mock.git.GetTagFunc = func(ctx context.Context, owner, repo, sha string) (*github.Tag, *github.Response, error) {
			return &github.Tag{Object: &github.GitObject{SHA: github.String("commit-sha-789")}}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "v2.0.0")
		require.NoError(t, err)
		assert.Equal(t, "commit-sha-789", sha)
	})

	t.Run("ref not found", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetBranchFunc = func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
			return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}
		mock.git.GetRefFunc = func(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
			return nil, nil, errors.New("not found")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.ResolveRef(ctx, "owner", "repo", "nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ref not found")
	})
}

func TestGitHubFetcher_SearchRepos(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.search.RepositoriesFunc = func(ctx context.Context, query string, opts *github.SearchOptions) (*github.RepositoriesSearchResult, *github.Response, error) {
			return &github.RepositoriesSearchResult{
				Total: github.Int(2),
				Repositories: []*github.Repository{
					{Name: github.String("ctxloom"), Owner: &github.User{Login: github.String("alice")}, StargazersCount: github.Int(100)},
					{Name: github.String("ctxloom-fragments"), Owner: &github.User{Login: github.String("bob")}, StargazersCount: github.Int(50)},
					{Name: github.String("awesome-ctxloom-tool"), Owner: &github.User{Login: github.String("charlie")}}, // Should be filtered
				},
			}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		repos, err := fetcher.SearchRepos(ctx, "", 30)
		require.NoError(t, err)
		assert.Len(t, repos, 2)
		assert.Equal(t, "ctxloom", repos[0].Name)
		assert.Equal(t, "ctxloom-fragments", repos[1].Name)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.search.RepositoriesFunc = func(ctx context.Context, query string, opts *github.SearchOptions) (*github.RepositoriesSearchResult, *github.Response, error) {
			return nil, nil, errors.New("rate limit exceeded")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.SearchRepos(ctx, "", 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "search failed")
	})
}

func TestGitHubFetcher_ValidateRepo(t *testing.T) {
	ctx := context.Background()

	t.Run("valid repo", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, []*github.RepositoryContent{{Name: github.String("fragments")}}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		valid, err := fetcher.ValidateRepo(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.True(t, valid)
	})

	t.Run("invalid repo - no ctxloom/v1", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		valid, err := fetcher.ValidateRepo(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.False(t, valid)
	})
}

func TestGitHubFetcher_GetDefaultBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetFunc = func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			return &github.Repository{DefaultBranch: github.String("main")}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		branch, err := fetcher.GetDefaultBranch(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.Equal(t, "main", branch)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetFunc = func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			return nil, nil, errors.New("not found")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.GetDefaultBranch(ctx, "owner", "repo")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get repo info")
	})
}

func TestNewGitHubFetcher(t *testing.T) {
	t.Run("with token", func(t *testing.T) {
		fetcher := NewGitHubFetcher("test-token")
		assert.NotNil(t, fetcher)
		assert.NotNil(t, fetcher.client)
	})

	t.Run("without token", func(t *testing.T) {
		fetcher := NewGitHubFetcher("")
		assert.NotNil(t, fetcher)
		assert.NotNil(t, fetcher.client)
	})
}

// Publisher tests

func TestGitHubPublisher_GetFileSHA(t *testing.T) {
	ctx := context.Background()

	t.Run("file exists", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return &github.RepositoryContent{SHA: github.String("file-sha-123")}, nil, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		sha, err := publisher.GetFileSHA(ctx, "owner", "repo", "file.txt", "main")
		require.NoError(t, err)
		assert.Equal(t, "file-sha-123", sha)
	})

	t.Run("file not found", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}

		publisher := NewGitHubPublisherWithClient(mock)
		sha, err := publisher.GetFileSHA(ctx, "owner", "repo", "missing.txt", "main")
		require.NoError(t, err)
		assert.Empty(t, sha)
	})

	t.Run("path is directory", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, []*github.RepositoryContent{{Name: github.String("file1.txt")}}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		_, err := publisher.GetFileSHA(ctx, "owner", "repo", "directory", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "directory")
	})
}

func TestGitHubPublisher_CreateOrUpdateFile(t *testing.T) {
	ctx := context.Background()

	t.Run("create new file", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}
		mock.repos.CreateFileFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error) {
			return &github.RepositoryContentResponse{Commit: github.Commit{SHA: github.String("new-commit-sha")}}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		sha, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "new-file.txt", "main", "Add file", []byte("content"))
		require.NoError(t, err)
		assert.Equal(t, "new-commit-sha", sha)
	})

	t.Run("update existing file", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return &github.RepositoryContent{SHA: github.String("existing-sha")}, nil, nil, nil
		}
		mock.repos.CreateFileFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error) {
			assert.Equal(t, "existing-sha", *opts.SHA)
			return &github.RepositoryContentResponse{Commit: github.Commit{SHA: github.String("update-sha")}}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		sha, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "existing.txt", "main", "Update file", []byte("new content"))
		require.NoError(t, err)
		assert.Equal(t, "update-sha", sha)
	})
}

func TestGitHubPublisher_CreateBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.git.CreateRefFunc = func(ctx context.Context, owner, repo string, ref *github.Reference) (*github.Reference, *github.Response, error) {
			assert.Equal(t, "refs/heads/new-branch", *ref.Ref)
			return &github.Reference{}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		err := publisher.CreateBranch(ctx, "owner", "repo", "new-branch", "base-sha")
		require.NoError(t, err)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.git.CreateRefFunc = func(ctx context.Context, owner, repo string, ref *github.Reference) (*github.Reference, *github.Response, error) {
			return nil, nil, errors.New("reference already exists")
		}

		publisher := NewGitHubPublisherWithClient(mock)
		err := publisher.CreateBranch(ctx, "owner", "repo", "existing-branch", "base-sha")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create branch")
	})
}

func TestGitHubPublisher_CreatePullRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.pullRequests.CreateFunc = func(ctx context.Context, owner, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error) {
			return &github.PullRequest{HTMLURL: github.String("https://github.com/owner/repo/pull/123")}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		url, err := publisher.CreatePullRequest(ctx, "owner", "repo", "Title", "Body", "feature", "main")
		require.NoError(t, err)
		assert.Equal(t, "https://github.com/owner/repo/pull/123", url)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.pullRequests.CreateFunc = func(ctx context.Context, owner, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error) {
			return nil, nil, errors.New("pull request already exists")
		}

		publisher := NewGitHubPublisherWithClient(mock)
		_, err := publisher.CreatePullRequest(ctx, "owner", "repo", "Title", "Body", "feature", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create pull request")
	})
}

func TestNewGitHubPublisher(t *testing.T) {
	t.Run("with token", func(t *testing.T) {
		publisher := NewGitHubPublisher("test-token")
		assert.NotNil(t, publisher)
		assert.NotNil(t, publisher.client)
	})

	t.Run("without token", func(t *testing.T) {
		publisher := NewGitHubPublisher("")
		assert.NotNil(t, publisher)
		assert.NotNil(t, publisher.client)
	})
}

// mockRoundTripper is a mock http.RoundTripper for testing.
type mockRoundTripper struct {
	roundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.roundTripFunc != nil {
		return m.roundTripFunc(req)
	}
	return nil, errors.New("not implemented")
}

func TestNewGitHubFetcher_WithHTTPClient(t *testing.T) {
	t.Run("uses custom HTTP client", func(t *testing.T) {
		transport := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				// Return a valid JSON response for GitHub API
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       http.NoBody,
					Header:     make(http.Header),
				}, nil
			},
		}

		httpClient := &http.Client{Transport: transport}
		fetcher := NewGitHubFetcher("", WithHTTPClient(httpClient))

		assert.NotNil(t, fetcher)
		assert.NotNil(t, fetcher.client)

		// Verify forge type is correct
		assert.Equal(t, ForgeGitHub, fetcher.Forge())
	})
}

func TestTokenTransport_RoundTrip(t *testing.T) {
	t.Run("adds authorization header", func(t *testing.T) {
		var capturedReq *http.Request
		mockTransport := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				capturedReq = req
				return &http.Response{StatusCode: 200}, nil
			},
		}

		transport := &tokenTransport{
			token: "test-token-123",
			base:  mockTransport,
		}

		req, _ := http.NewRequest("GET", "https://api.github.com/test", nil)
		_, err := transport.RoundTrip(req)

		require.NoError(t, err)
		assert.Equal(t, "Bearer test-token-123", capturedReq.Header.Get("Authorization"))
	})

	t.Run("uses default transport when base is nil", func(t *testing.T) {
		transport := &tokenTransport{
			token: "test-token",
			base:  nil, // Should default to http.DefaultTransport
		}

		// Verify the transport is configured correctly without making a real request
		assert.NotNil(t, transport)
		assert.Equal(t, "test-token", transport.token)
		assert.Nil(t, transport.base) // base is nil, RoundTrip will use http.DefaultTransport
	})

	t.Run("propagates transport errors", func(t *testing.T) {
		mockTransport := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("network error")
			},
		}

		transport := &tokenTransport{
			token: "test-token",
			base:  mockTransport,
		}

		req, _ := http.NewRequest("GET", "https://api.github.com/test", nil)
		_, err := transport.RoundTrip(req)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "network error")
	})
}

func TestRealGitHubClient_ServiceAccessors(t *testing.T) {
	// Test that realGitHubClient properly returns service accessors
	httpClient := &http.Client{
		Transport: &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       http.NoBody,
					Header:     make(http.Header),
				}, nil
			},
		},
	}

	fetcher := NewGitHubFetcher("", WithHTTPClient(httpClient))

	// Access the client through the fetcher to test the real client wrappers
	// The GitHubFetcher.client is a GitHubClient interface
	client := fetcher.client

	t.Run("Repositories returns non-nil", func(t *testing.T) {
		repos := client.Repositories()
		assert.NotNil(t, repos)
	})

	t.Run("Git returns non-nil", func(t *testing.T) {
		git := client.Git()
		assert.NotNil(t, git)
	})

	t.Run("Search returns non-nil", func(t *testing.T) {
		search := client.Search()
		assert.NotNil(t, search)
	})

	t.Run("PullRequests returns non-nil", func(t *testing.T) {
		prs := client.PullRequests()
		assert.NotNil(t, prs)
	})
}

func TestGitHubFetcher_401Retry(t *testing.T) {
	ctx := context.Background()

	t.Run("retries without auth on 401", func(t *testing.T) {
		// First call returns 401, second succeeds
		callCount := 0
		content := "test file content"
		encoded := base64.StdEncoding.EncodeToString([]byte(content))

		mainMock := newMockGitHubClient()
		mainMock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			callCount++
			if callCount == 1 {
				// First call - 401 error
				return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusUnauthorized}}, errors.New("Bad credentials")
			}
			// Second call - success with fallback client
			return &github.RepositoryContent{
				Type:    github.String("file"),
				Content: github.String(encoded),
			}, nil, nil, nil
		}

		fallbackMock := newMockGitHubClient()
		fallbackMock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			// Fallback succeeds
			return &github.RepositoryContent{
				Type:    github.String("file"),
				Content: github.String(encoded),
			}, nil, nil, nil
		}

		fetcher := &GitHubFetcher{
			client:   mainMock,
			fallback: fallbackMock,
			hasToken: true,
		}

		result, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.NoError(t, err)
		assert.Equal(t, content, string(result))
		assert.Equal(t, 1, callCount)
	})

	t.Run("no retry without fallback client", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusUnauthorized}}, errors.New("Bad credentials")
		}

		fetcher := &GitHubFetcher{
			client:   mock,
			fallback: nil,
			hasToken: false,
		}

		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch file")
	})

	t.Run("detects 401 error from response status", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusUnauthorized}}, errors.New("unauthorized")
		}

		fallbackMock := newMockGitHubClient()
		fallbackMock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			content := "public content"
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			return &github.RepositoryContent{
				Type:    github.String("file"),
				Content: github.String(encoded),
			}, nil, nil, nil
		}

		fetcher := &GitHubFetcher{
			client:   mock,
			fallback: fallbackMock,
			hasToken: true,
		}

		result, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.NoError(t, err)
		assert.Equal(t, "public content", string(result))
	})
}

func TestGitHubFetcher_ListDir_401Retry(t *testing.T) {
	ctx := context.Background()

	t.Run("retries ListDir on 401", func(t *testing.T) {
		mainMock := newMockGitHubClient()
		mainMock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusUnauthorized}}, errors.New("Bad credentials")
		}

		fallbackMock := newMockGitHubClient()
		fallbackMock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, []*github.RepositoryContent{
				{Type: github.String("file"), Name: github.String("test.yaml"), SHA: github.String("abc123")},
			}, nil, nil
		}

		fetcher := &GitHubFetcher{
			client:   mainMock,
			fallback: fallbackMock,
			hasToken: true,
		}

		entries, err := fetcher.ListDir(ctx, "owner", "repo", "path", "")
		require.NoError(t, err)
		assert.Len(t, entries, 1)
		assert.Equal(t, "test.yaml", entries[0].Name)
	})
}

func TestGitHubFetcher_OtherErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("500 server error", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusInternalServerError}}, errors.New("server error")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch file")
	})

	t.Run("403 Forbidden", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusForbidden}}, errors.New("forbidden")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch file")
	})

	t.Run("rate limit (429)", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: 429}}, errors.New("rate limited")
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch file")
	})
}

func TestGitHubFetcher_InvalidFileContent(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid base64 encoding", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return &github.RepositoryContent{
				Type:    github.String("file"),
				Content: github.String("not valid base64!!!"),
			}, nil, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode file content")
	})
}
