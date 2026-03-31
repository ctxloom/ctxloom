package remote

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// Mock implementations

type mockGitLabRepositoryFilesService struct {
	GetRawFileFunc func(pid interface{}, fileName string, opt *gitlab.GetRawFileOptions, options ...gitlab.RequestOptionFunc) ([]byte, *gitlab.Response, error)
	GetFileFunc    func(pid interface{}, fileName string, opt *gitlab.GetFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.File, *gitlab.Response, error)
	CreateFileFunc func(pid interface{}, fileName string, opt *gitlab.CreateFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.FileInfo, *gitlab.Response, error)
	UpdateFileFunc func(pid interface{}, fileName string, opt *gitlab.UpdateFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.FileInfo, *gitlab.Response, error)
}

func (m *mockGitLabRepositoryFilesService) GetRawFile(pid interface{}, fileName string, opt *gitlab.GetRawFileOptions, options ...gitlab.RequestOptionFunc) ([]byte, *gitlab.Response, error) {
	if m.GetRawFileFunc != nil {
		return m.GetRawFileFunc(pid, fileName, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitLabRepositoryFilesService) GetFile(pid interface{}, fileName string, opt *gitlab.GetFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.File, *gitlab.Response, error) {
	if m.GetFileFunc != nil {
		return m.GetFileFunc(pid, fileName, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitLabRepositoryFilesService) CreateFile(pid interface{}, fileName string, opt *gitlab.CreateFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.FileInfo, *gitlab.Response, error) {
	if m.CreateFileFunc != nil {
		return m.CreateFileFunc(pid, fileName, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitLabRepositoryFilesService) UpdateFile(pid interface{}, fileName string, opt *gitlab.UpdateFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.FileInfo, *gitlab.Response, error) {
	if m.UpdateFileFunc != nil {
		return m.UpdateFileFunc(pid, fileName, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabRepositoriesService struct {
	ListTreeFunc func(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error)
}

func (m *mockGitLabRepositoriesService) ListTree(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error) {
	if m.ListTreeFunc != nil {
		return m.ListTreeFunc(pid, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabCommitsService struct {
	GetCommitFunc func(pid interface{}, sha string, opt *gitlab.GetCommitOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Commit, *gitlab.Response, error)
}

func (m *mockGitLabCommitsService) GetCommit(pid interface{}, sha string, opt *gitlab.GetCommitOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Commit, *gitlab.Response, error) {
	if m.GetCommitFunc != nil {
		return m.GetCommitFunc(pid, sha, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabBranchesService struct {
	GetBranchFunc    func(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error)
	CreateBranchFunc func(pid interface{}, opt *gitlab.CreateBranchOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error)
}

func (m *mockGitLabBranchesService) GetBranch(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
	if m.GetBranchFunc != nil {
		return m.GetBranchFunc(pid, branch, options...)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitLabBranchesService) CreateBranch(pid interface{}, opt *gitlab.CreateBranchOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
	if m.CreateBranchFunc != nil {
		return m.CreateBranchFunc(pid, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabTagsService struct {
	GetTagFunc func(pid interface{}, tag string, options ...gitlab.RequestOptionFunc) (*gitlab.Tag, *gitlab.Response, error)
}

func (m *mockGitLabTagsService) GetTag(pid interface{}, tag string, options ...gitlab.RequestOptionFunc) (*gitlab.Tag, *gitlab.Response, error) {
	if m.GetTagFunc != nil {
		return m.GetTagFunc(pid, tag, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabProjectsService struct {
	ListProjectsFunc func(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error)
	GetProjectFunc   func(pid interface{}, opt *gitlab.GetProjectOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Project, *gitlab.Response, error)
}

func (m *mockGitLabProjectsService) ListProjects(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error) {
	if m.ListProjectsFunc != nil {
		return m.ListProjectsFunc(opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

func (m *mockGitLabProjectsService) GetProject(pid interface{}, opt *gitlab.GetProjectOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Project, *gitlab.Response, error) {
	if m.GetProjectFunc != nil {
		return m.GetProjectFunc(pid, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabMergeRequestsService struct {
	CreateMergeRequestFunc func(pid interface{}, opt *gitlab.CreateMergeRequestOptions, options ...gitlab.RequestOptionFunc) (*gitlab.MergeRequest, *gitlab.Response, error)
}

func (m *mockGitLabMergeRequestsService) CreateMergeRequest(pid interface{}, opt *gitlab.CreateMergeRequestOptions, options ...gitlab.RequestOptionFunc) (*gitlab.MergeRequest, *gitlab.Response, error) {
	if m.CreateMergeRequestFunc != nil {
		return m.CreateMergeRequestFunc(pid, opt, options...)
	}
	return nil, nil, errors.New("not implemented")
}

type mockGitLabClient struct {
	repoFiles     *mockGitLabRepositoryFilesService
	repos         *mockGitLabRepositoriesService
	commits       *mockGitLabCommitsService
	branches      *mockGitLabBranchesService
	tags          *mockGitLabTagsService
	projects      *mockGitLabProjectsService
	mergeRequests *mockGitLabMergeRequestsService
}

func newMockGitLabClient() *mockGitLabClient {
	return &mockGitLabClient{
		repoFiles:     &mockGitLabRepositoryFilesService{},
		repos:         &mockGitLabRepositoriesService{},
		commits:       &mockGitLabCommitsService{},
		branches:      &mockGitLabBranchesService{},
		tags:          &mockGitLabTagsService{},
		projects:      &mockGitLabProjectsService{},
		mergeRequests: &mockGitLabMergeRequestsService{},
	}
}

func (m *mockGitLabClient) RepositoryFiles() GitLabRepositoryFilesService { return m.repoFiles }
func (m *mockGitLabClient) Repositories() GitLabRepositoriesService       { return m.repos }
func (m *mockGitLabClient) Commits() GitLabCommitsService                 { return m.commits }
func (m *mockGitLabClient) Branches() GitLabBranchesService               { return m.branches }
func (m *mockGitLabClient) Tags() GitLabTagsService                       { return m.tags }
func (m *mockGitLabClient) Projects() GitLabProjectsService               { return m.projects }
func (m *mockGitLabClient) MergeRequests() GitLabMergeRequestsService     { return m.mergeRequests }

// Tests

func TestGitLabFetcher_Forge(t *testing.T) {
	fetcher, _ := NewGitLabFetcher("", "")
	assert.Equal(t, ForgeGitLab, fetcher.Forge())
}

func TestProjectID(t *testing.T) {
	assert.Equal(t, "owner%2Frepo", projectID("owner", "repo"))
	assert.Equal(t, "org%2Fsubgroup%2Frepo", projectID("org/subgroup", "repo"))
}

func TestGitLabFetcher_FetchFile(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		content := "test file content"
		mock := newMockGitLabClient()
		mock.repoFiles.GetRawFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetRawFileOptions, options ...gitlab.RequestOptionFunc) ([]byte, *gitlab.Response, error) {
			return []byte(content), nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		result, err := fetcher.FetchFile(ctx, "owner", "repo", "path/to/file.txt", "")
		require.NoError(t, err)
		assert.Equal(t, content, string(result))
	})

	t.Run("with ref", func(t *testing.T) {
		var capturedRef string
		mock := newMockGitLabClient()
		mock.repoFiles.GetRawFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetRawFileOptions, options ...gitlab.RequestOptionFunc) ([]byte, *gitlab.Response, error) {
			if opt != nil && opt.Ref != nil {
				capturedRef = *opt.Ref
			}
			return []byte("content"), nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", capturedRef)
	})

	t.Run("file not found", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repoFiles.GetRawFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetRawFileOptions, options ...gitlab.RequestOptionFunc) ([]byte, *gitlab.Response, error) {
			return nil, &gitlab.Response{Response: newHTTPResponse(404)}, errors.New("not found")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "missing.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "file not found")
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repoFiles.GetRawFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetRawFileOptions, options ...gitlab.RequestOptionFunc) ([]byte, *gitlab.Response, error) {
			return nil, &gitlab.Response{Response: newHTTPResponse(500)}, errors.New("server error")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.FetchFile(ctx, "owner", "repo", "file.txt", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch file")
	})
}

func TestGitLabFetcher_ListDir(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repos.ListTreeFunc = func(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error) {
			return []*gitlab.TreeNode{
				{ID: "abc123", Name: "fragment.yaml", Type: "blob"},
				{ID: "def456", Name: "fragments", Type: "tree"},
			}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		entries, err := fetcher.ListDir(ctx, "owner", "repo", "ctxloom/v1", "")
		require.NoError(t, err)
		assert.Len(t, entries, 2)
		assert.Equal(t, "fragment.yaml", entries[0].Name)
		assert.False(t, entries[0].IsDir)
		assert.Equal(t, "fragments", entries[1].Name)
		assert.True(t, entries[1].IsDir)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repos.ListTreeFunc = func(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error) {
			return nil, nil, errors.New("forbidden")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.ListDir(ctx, "owner", "repo", "path", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list directory")
	})
}

func TestGitLabFetcher_ResolveRef(t *testing.T) {
	ctx := context.Background()

	t.Run("resolve SHA", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.commits.GetCommitFunc = func(pid interface{}, sha string, opt *gitlab.GetCommitOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Commit, *gitlab.Response, error) {
			return &gitlab.Commit{ID: "abc1234567890abcdef"}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "abc1234")
		require.NoError(t, err)
		assert.Equal(t, "abc1234567890abcdef", sha)
	})

	t.Run("resolve branch", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.commits.GetCommitFunc = func(pid interface{}, sha string, opt *gitlab.GetCommitOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Commit, *gitlab.Response, error) {
			return nil, nil, errors.New("not a commit")
		}
		mock.branches.GetBranchFunc = func(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return &gitlab.Branch{Commit: &gitlab.Commit{ID: "branch-sha-123"}}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "main")
		require.NoError(t, err)
		assert.Equal(t, "branch-sha-123", sha)
	})

	t.Run("resolve tag", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.branches.GetBranchFunc = func(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return nil, nil, errors.New("not found")
		}
		mock.tags.GetTagFunc = func(pid interface{}, tag string, options ...gitlab.RequestOptionFunc) (*gitlab.Tag, *gitlab.Response, error) {
			return &gitlab.Tag{Commit: &gitlab.Commit{ID: "tag-sha-456"}}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "v1.0.0")
		require.NoError(t, err)
		assert.Equal(t, "tag-sha-456", sha)
	})

	t.Run("ref not found", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.branches.GetBranchFunc = func(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return nil, nil, errors.New("not found")
		}
		mock.tags.GetTagFunc = func(pid interface{}, tag string, options ...gitlab.RequestOptionFunc) (*gitlab.Tag, *gitlab.Response, error) {
			return nil, nil, errors.New("not found")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.ResolveRef(ctx, "owner", "repo", "nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ref not found")
	})
}

func TestGitLabFetcher_SearchRepos(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		now := time.Now()
		mock := newMockGitLabClient()
		mock.projects.ListProjectsFunc = func(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error) {
			return []*gitlab.Project{
				{Path: "ctxloom", Namespace: &gitlab.ProjectNamespace{Path: "alice"}, StarCount: 100, LastActivityAt: &now},
				{Path: "ctxloom-fragments", Namespace: &gitlab.ProjectNamespace{Path: "bob"}, StarCount: 50, LastActivityAt: &now},
				{Path: "awesome-ctxloom-tool", Namespace: &gitlab.ProjectNamespace{Path: "charlie"}, LastActivityAt: &now}, // Should be filtered
			}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		repos, err := fetcher.SearchRepos(ctx, "", 30)
		require.NoError(t, err)
		assert.Len(t, repos, 2)
		assert.Equal(t, "ctxloom", repos[0].Name)
		assert.Equal(t, "ctxloom-fragments", repos[1].Name)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.projects.ListProjectsFunc = func(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error) {
			return nil, nil, errors.New("rate limit exceeded")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.SearchRepos(ctx, "", 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "search failed")
	})
}

func TestGitLabFetcher_ValidateRepo(t *testing.T) {
	ctx := context.Background()

	t.Run("valid repo", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repos.ListTreeFunc = func(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error) {
			return []*gitlab.TreeNode{{Name: "fragments", Type: "tree"}}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		valid, err := fetcher.ValidateRepo(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.True(t, valid)
	})

	t.Run("invalid repo - empty ctxloom/v1", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repos.ListTreeFunc = func(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error) {
			return []*gitlab.TreeNode{}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		valid, err := fetcher.ValidateRepo(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.False(t, valid)
	})

	t.Run("invalid repo - no ctxloom/v1", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repos.ListTreeFunc = func(pid interface{}, opt *gitlab.ListTreeOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.TreeNode, *gitlab.Response, error) {
			return nil, &gitlab.Response{Response: newHTTPResponse(404)}, errors.New("not found")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		valid, err := fetcher.ValidateRepo(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.False(t, valid)
	})
}

func TestGitLabFetcher_GetDefaultBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.projects.GetProjectFunc = func(pid interface{}, opt *gitlab.GetProjectOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Project, *gitlab.Response, error) {
			return &gitlab.Project{DefaultBranch: "main"}, nil, nil
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		branch, err := fetcher.GetDefaultBranch(ctx, "owner", "repo")
		require.NoError(t, err)
		assert.Equal(t, "main", branch)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.projects.GetProjectFunc = func(pid interface{}, opt *gitlab.GetProjectOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Project, *gitlab.Response, error) {
			return nil, nil, errors.New("not found")
		}

		fetcher := NewGitLabFetcherWithClient(mock, "https://gitlab.com")
		_, err := fetcher.GetDefaultBranch(ctx, "owner", "repo")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get project info")
	})
}

func TestNewGitLabFetcher(t *testing.T) {
	t.Run("with defaults", func(t *testing.T) {
		fetcher, err := NewGitLabFetcher("", "")
		require.NoError(t, err)
		assert.NotNil(t, fetcher)
		assert.Equal(t, "https://gitlab.com", fetcher.baseURL)
	})

	t.Run("with custom url", func(t *testing.T) {
		fetcher, err := NewGitLabFetcher("https://gitlab.example.com", "")
		require.NoError(t, err)
		assert.NotNil(t, fetcher)
		assert.Equal(t, "https://gitlab.example.com", fetcher.baseURL)
	})

	t.Run("with token", func(t *testing.T) {
		fetcher, err := NewGitLabFetcher("", "test-token")
		require.NoError(t, err)
		assert.NotNil(t, fetcher)
	})
}

// Publisher tests

func TestGitLabPublisher_GetFileSHA(t *testing.T) {
	ctx := context.Background()

	t.Run("file exists", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repoFiles.GetFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.File, *gitlab.Response, error) {
			return &gitlab.File{BlobID: "file-blob-id-123"}, nil, nil
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		sha, err := publisher.GetFileSHA(ctx, "owner", "repo", "file.txt", "main")
		require.NoError(t, err)
		assert.Equal(t, "file-blob-id-123", sha)
	})

	t.Run("file not found", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repoFiles.GetFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.File, *gitlab.Response, error) {
			return nil, &gitlab.Response{Response: newHTTPResponse(404)}, errors.New("not found")
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		sha, err := publisher.GetFileSHA(ctx, "owner", "repo", "missing.txt", "main")
		require.NoError(t, err)
		assert.Empty(t, sha)
	})
}

func TestGitLabPublisher_CreateOrUpdateFile(t *testing.T) {
	ctx := context.Background()

	t.Run("create new file", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repoFiles.GetFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.File, *gitlab.Response, error) {
			return nil, &gitlab.Response{Response: newHTTPResponse(404)}, errors.New("not found")
		}
		mock.repoFiles.CreateFileFunc = func(pid interface{}, fileName string, opt *gitlab.CreateFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.FileInfo, *gitlab.Response, error) {
			return &gitlab.FileInfo{FilePath: "new-file.txt"}, nil, nil
		}
		mock.branches.GetBranchFunc = func(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return &gitlab.Branch{Commit: &gitlab.Commit{ID: "new-commit-sha"}}, nil, nil
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		sha, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "new-file.txt", "main", "Add file", []byte("content"))
		require.NoError(t, err)
		assert.Equal(t, "new-commit-sha", sha)
	})

	t.Run("update existing file", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.repoFiles.GetFileFunc = func(pid interface{}, fileName string, opt *gitlab.GetFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.File, *gitlab.Response, error) {
			return &gitlab.File{BlobID: "existing-blob-id"}, nil, nil
		}
		mock.repoFiles.UpdateFileFunc = func(pid interface{}, fileName string, opt *gitlab.UpdateFileOptions, options ...gitlab.RequestOptionFunc) (*gitlab.FileInfo, *gitlab.Response, error) {
			return &gitlab.FileInfo{FilePath: "existing.txt"}, nil, nil
		}
		mock.branches.GetBranchFunc = func(pid interface{}, branch string, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return &gitlab.Branch{Commit: &gitlab.Commit{ID: "update-commit-sha"}}, nil, nil
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		sha, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "existing.txt", "main", "Update file", []byte("new content"))
		require.NoError(t, err)
		assert.Equal(t, "update-commit-sha", sha)
	})
}

func TestGitLabPublisher_CreateBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.branches.CreateBranchFunc = func(pid interface{}, opt *gitlab.CreateBranchOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return &gitlab.Branch{Name: "new-branch"}, nil, nil
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		err := publisher.CreateBranch(ctx, "owner", "repo", "new-branch", "base-sha")
		require.NoError(t, err)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.branches.CreateBranchFunc = func(pid interface{}, opt *gitlab.CreateBranchOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Branch, *gitlab.Response, error) {
			return nil, nil, errors.New("branch already exists")
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		err := publisher.CreateBranch(ctx, "owner", "repo", "existing-branch", "base-sha")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create branch")
	})
}

func TestGitLabPublisher_CreatePullRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.mergeRequests.CreateMergeRequestFunc = func(pid interface{}, opt *gitlab.CreateMergeRequestOptions, options ...gitlab.RequestOptionFunc) (*gitlab.MergeRequest, *gitlab.Response, error) {
			return &gitlab.MergeRequest{
				BasicMergeRequest: gitlab.BasicMergeRequest{
					WebURL: "https://gitlab.com/owner/repo/-/merge_requests/123",
				},
			}, nil, nil
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		url, err := publisher.CreatePullRequest(ctx, "owner", "repo", "Title", "Body", "feature", "main")
		require.NoError(t, err)
		assert.Equal(t, "https://gitlab.com/owner/repo/-/merge_requests/123", url)
	})

	t.Run("api error", func(t *testing.T) {
		mock := newMockGitLabClient()
		mock.mergeRequests.CreateMergeRequestFunc = func(pid interface{}, opt *gitlab.CreateMergeRequestOptions, options ...gitlab.RequestOptionFunc) (*gitlab.MergeRequest, *gitlab.Response, error) {
			return nil, nil, errors.New("merge request already exists")
		}

		publisher := NewGitLabPublisherWithClient(mock, "https://gitlab.com")
		_, err := publisher.CreatePullRequest(ctx, "owner", "repo", "Title", "Body", "feature", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create merge request")
	})
}

func TestNewGitLabPublisher(t *testing.T) {
	t.Run("with defaults", func(t *testing.T) {
		publisher := NewGitLabPublisher("", "")
		assert.NotNil(t, publisher)
		assert.Equal(t, "https://gitlab.com", publisher.baseURL)
	})

	t.Run("with custom url", func(t *testing.T) {
		publisher := NewGitLabPublisher("https://gitlab.example.com", "")
		assert.NotNil(t, publisher)
		assert.Equal(t, "https://gitlab.example.com", publisher.baseURL)
	})

	t.Run("with token", func(t *testing.T) {
		publisher := NewGitLabPublisher("", "test-token")
		assert.NotNil(t, publisher)
	})
}

// Helper to create HTTP response
func newHTTPResponse(statusCode int) *http.Response {
	return &http.Response{StatusCode: statusCode}
}

func TestRealGitLabClient_ServiceAccessors(t *testing.T) {
	// Test that realGitLabClient properly returns service accessors
	// Create a fetcher with defaults (will use real client internally)
	fetcher, err := NewGitLabFetcher("https://gitlab.com", "test-token")
	require.NoError(t, err)

	client := fetcher.client

	t.Run("RepositoryFiles returns non-nil", func(t *testing.T) {
		files := client.RepositoryFiles()
		assert.NotNil(t, files)
	})

	t.Run("Repositories returns non-nil", func(t *testing.T) {
		repos := client.Repositories()
		assert.NotNil(t, repos)
	})

	t.Run("Commits returns non-nil", func(t *testing.T) {
		commits := client.Commits()
		assert.NotNil(t, commits)
	})

	t.Run("Branches returns non-nil", func(t *testing.T) {
		branches := client.Branches()
		assert.NotNil(t, branches)
	})

	t.Run("Tags returns non-nil", func(t *testing.T) {
		tags := client.Tags()
		assert.NotNil(t, tags)
	})

	t.Run("Projects returns non-nil", func(t *testing.T) {
		projects := client.Projects()
		assert.NotNil(t, projects)
	})

	t.Run("MergeRequests returns non-nil", func(t *testing.T) {
		mrs := client.MergeRequests()
		assert.NotNil(t, mrs)
	})
}

func TestNewGitLabFetcher_WithOptions(t *testing.T) {
	t.Run("creates with defaults", func(t *testing.T) {
		fetcher, err := NewGitLabFetcher("", "")
		require.NoError(t, err)
		assert.NotNil(t, fetcher)
		assert.Equal(t, "https://gitlab.com", fetcher.baseURL)
	})

	t.Run("uses custom base URL", func(t *testing.T) {
		fetcher, err := NewGitLabFetcher("https://gitlab.example.com", "")
		require.NoError(t, err)
		assert.NotNil(t, fetcher)
		assert.Equal(t, "https://gitlab.example.com", fetcher.baseURL)
	})

	t.Run("with custom HTTP client", func(t *testing.T) {
		customClient := &http.Client{
			Timeout: 30,
		}
		fetcher, err := NewGitLabFetcher("https://gitlab.com", "", WithGitLabHTTPClient(customClient))
		require.NoError(t, err)
		assert.NotNil(t, fetcher)
		assert.Equal(t, "https://gitlab.com", fetcher.baseURL)
	})
}
