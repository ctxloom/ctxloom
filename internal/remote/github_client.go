package remote

import (
	"context"
	"net/http"

	"github.com/google/go-github/v60/github"
)

// GitHubRepositoriesService defines the GitHub Repositories API methods we use.
type GitHubRepositoriesService interface {
	GetContents(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error)
	GetCommit(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error)
	GetBranch(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error)
	Get(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error)
	CreateFile(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error)
}

// GitHubGitService defines the GitHub Git API methods we use.
type GitHubGitService interface {
	GetRef(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error)
	GetTag(ctx context.Context, owner, repo, sha string) (*github.Tag, *github.Response, error)
	CreateRef(ctx context.Context, owner, repo string, ref *github.Reference) (*github.Reference, *github.Response, error)
}

// GitHubSearchService defines the GitHub Search API methods we use.
type GitHubSearchService interface {
	Repositories(ctx context.Context, query string, opts *github.SearchOptions) (*github.RepositoriesSearchResult, *github.Response, error)
}

// GitHubPullRequestsService defines the GitHub Pull Requests API methods we use.
type GitHubPullRequestsService interface {
	Create(ctx context.Context, owner, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error)
}

// GitHubClient wraps the services we need from the GitHub client.
type GitHubClient interface {
	Repositories() GitHubRepositoriesService
	Git() GitHubGitService
	Search() GitHubSearchService
	PullRequests() GitHubPullRequestsService
}

// realGitHubClient wraps the actual github.Client.
type realGitHubClient struct {
	client *github.Client
}

// newRealGitHubClient builds a GitHub API client. A non-empty apiURL points
// it at a GitHub Enterprise REST endpoint (base and upload share the URL); an
// invalid apiURL falls back to the public github.com client rather than
// failing — a bad endpoint should degrade, not crash startup.
func newRealGitHubClient(httpClient *http.Client, apiURL string) GitHubClient {
	client := github.NewClient(httpClient)
	if apiURL != "" {
		if ent, err := client.WithEnterpriseURLs(apiURL, apiURL); err == nil {
			client = ent
		}
	}
	return &realGitHubClient{client: client}
}

func (c *realGitHubClient) Repositories() GitHubRepositoriesService {
	return c.client.Repositories
}

func (c *realGitHubClient) Git() GitHubGitService {
	return c.client.Git
}

func (c *realGitHubClient) Search() GitHubSearchService {
	return c.client.Search
}

func (c *realGitHubClient) PullRequests() GitHubPullRequestsService {
	return c.client.PullRequests
}
