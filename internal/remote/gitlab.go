package remote

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// GitLabFetcher implements Fetcher for GitLab repositories.
type GitLabFetcher struct {
	client  GitLabClient
	baseURL string // e.g., "https://gitlab.com" or self-hosted URL
}

// GitLabFetcherOption configures a GitLabFetcher.
type GitLabFetcherOption func(*gitLabFetcherConfig)

type gitLabFetcherConfig struct {
	clientOpts []gitlab.ClientOptionFunc
}

// WithGitLabHTTPClient sets a custom HTTP client (for testing).
func WithGitLabHTTPClient(client *http.Client) GitLabFetcherOption {
	return func(c *gitLabFetcherConfig) {
		c.clientOpts = append(c.clientOpts, gitlab.WithHTTPClient(client))
	}
}

// NewGitLabFetcher creates a new GitLab fetcher.
// baseURL should be the GitLab instance URL (e.g., "https://gitlab.com").
// If token is empty, it will try GITLAB_TOKEN env var.
func NewGitLabFetcher(baseURL, token string, opts ...GitLabFetcherOption) (*GitLabFetcher, error) {
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	if token == "" {
		token = os.Getenv("GITLAB_TOKEN")
	}

	cfg := &gitLabFetcherConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	clientOpts := []gitlab.ClientOptionFunc{
		gitlab.WithBaseURL(baseURL),
	}
	clientOpts = append(clientOpts, cfg.clientOpts...)

	client, err := newRealGitLabClient(token, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitLab client: %w", err)
	}

	return &GitLabFetcher{
		client:  client,
		baseURL: baseURL,
	}, nil
}

// NewGitLabFetcherWithClient creates a GitLabFetcher with a custom client (for testing).
func NewGitLabFetcherWithClient(client GitLabClient, baseURL string) *GitLabFetcher {
	return &GitLabFetcher{client: client, baseURL: baseURL}
}

// Forge returns the forge type.
func (f *GitLabFetcher) Forge() ForgeType {
	return ForgeGitLab
}

// projectID returns the project identifier for API calls.
func projectID(owner, repo string) string {
	return url.PathEscape(owner + "/" + repo)
}

// FetchFile retrieves raw file content from a GitLab repository.
func (f *GitLabFetcher) FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	opts := &gitlab.GetRawFileOptions{}
	if ref != "" {
		opts.Ref = &ref
	}

	content, resp, err := f.client.RepositoryFiles().GetRawFile(projectID(owner, repo), path, opts, gitlab.WithContext(ctx))
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, fmt.Errorf("file not found: %s/%s/%s", owner, repo, path)
		}
		return nil, fmt.Errorf("failed to fetch file: %w", err)
	}

	return content, nil
}

// ListDir lists directory contents at the specified path.
func (f *GitLabFetcher) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	opts := &gitlab.ListTreeOptions{
		Path: &path,
	}
	if ref != "" {
		opts.Ref = &ref
	}

	items, _, err := f.client.Repositories().ListTree(projectID(owner, repo), opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list directory: %w", err)
	}

	entries := make([]DirEntry, 0, len(items))
	for _, item := range items {
		entry := DirEntry{
			Name:  item.Name,
			IsDir: item.Type == "tree",
			SHA:   item.ID,
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// ResolveRef converts a git reference to a commit SHA.
func (f *GitLabFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	pid := projectID(owner, repo)

	// Try as a commit SHA first
	if len(ref) >= 7 && len(ref) <= 40 {
		commit, _, err := f.client.Commits().GetCommit(pid, ref, nil, gitlab.WithContext(ctx))
		if err == nil {
			return commit.ID, nil
		}
	}

	// Try as a branch
	branch, _, err := f.client.Branches().GetBranch(pid, ref, gitlab.WithContext(ctx))
	if err == nil {
		return branch.Commit.ID, nil
	}

	// Try as a tag
	tag, _, err := f.client.Tags().GetTag(pid, ref, gitlab.WithContext(ctx))
	if err == nil {
		return tag.Commit.ID, nil
	}

	return "", fmt.Errorf("ref not found: %s", ref)
}

// SearchRepos finds ctxloom repositories.
func (f *GitLabFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	if limit <= 0 {
		limit = 30
	}

	// Search for projects with "ctxloom" in name
	searchQuery := "ctxloom"
	if query != "" {
		searchQuery = fmt.Sprintf("ctxloom %s", query)
	}

	orderBy := "last_activity_at"
	sort := "desc"
	perPage := int64(limit)
	opts := &gitlab.ListProjectsOptions{
		Search:  &searchQuery,
		OrderBy: &orderBy,
		Sort:    &sort,
		ListOptions: gitlab.ListOptions{
			PerPage: perPage,
		},
	}

	projects, _, err := f.client.Projects().ListProjects(opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	repos := make([]RepoInfo, 0, len(projects))
	for _, p := range projects {
		name := p.Path
		// Filter to only repos named "ctxloom" or "ctxloom-*"
		if name != "ctxloom" && !strings.HasPrefix(name, "ctxloom-") {
			continue
		}

		repos = append(repos, RepoInfo{
			Owner:       p.Namespace.Path,
			Name:        name,
			Description: p.Description,
			Stars:       int(p.StarCount),
			URL:         p.WebURL,
			Topics:      p.Topics,
			UpdatedAt:   *p.LastActivityAt,
			Forge:       ForgeGitLab,
		})
	}

	return repos, nil
}

// ValidateRepo checks if a repository has valid ctxloom structure.
func (f *GitLabFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	path := "ctxloom/v1"
	opts := &gitlab.ListTreeOptions{
		Path: &path,
	}

	items, resp, err := f.client.Repositories().ListTree(projectID(owner, repo), opts, gitlab.WithContext(ctx))
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check repo structure: %w", err)
	}

	return len(items) > 0, nil
}

// GetDefaultBranch returns the default branch name.
func (f *GitLabFetcher) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	project, _, err := f.client.Projects().GetProject(projectID(owner, repo), nil, gitlab.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("failed to get project info: %w", err)
	}
	return project.DefaultBranch, nil
}

// GitLabPublisher implements Publisher for GitLab repositories.
type GitLabPublisher struct {
	client  GitLabClient
	baseURL string
}

// NewGitLabPublisher creates a new GitLab publisher.
func NewGitLabPublisher(baseURL, token string) *GitLabPublisher {
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	if token == "" {
		token = os.Getenv("GITLAB_TOKEN")
	}

	opts := []gitlab.ClientOptionFunc{
		gitlab.WithBaseURL(baseURL),
	}

	client, _ := newRealGitLabClient(token, opts...)

	return &GitLabPublisher{
		client:  client,
		baseURL: baseURL,
	}
}

// NewGitLabPublisherWithClient creates a GitLabPublisher with a custom client (for testing).
func NewGitLabPublisherWithClient(client GitLabClient, baseURL string) *GitLabPublisher {
	return &GitLabPublisher{client: client, baseURL: baseURL}
}

// CreateOrUpdateFile creates or updates a file in a repository.
func (p *GitLabPublisher) CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch, message string, content []byte) (string, error) {
	pid := projectID(owner, repo)

	// Check if file exists
	existingSHA, _ := p.GetFileSHA(ctx, owner, repo, path, branch)

	if existingSHA != "" {
		// Update existing file
		opts := &gitlab.UpdateFileOptions{
			Branch:        &branch,
			Content:       gitlab.Ptr(string(content)),
			CommitMessage: &message,
		}
		_, _, err := p.client.RepositoryFiles().UpdateFile(pid, path, opts, gitlab.WithContext(ctx))
		if err != nil {
			return "", fmt.Errorf("failed to update file: %w", err)
		}
	} else {
		// Create new file
		opts := &gitlab.CreateFileOptions{
			Branch:        &branch,
			Content:       gitlab.Ptr(string(content)),
			CommitMessage: &message,
		}
		_, _, err := p.client.RepositoryFiles().CreateFile(pid, path, opts, gitlab.WithContext(ctx))
		if err != nil {
			return "", fmt.Errorf("failed to create file: %w", err)
		}
	}

	// Get the commit SHA from the branch head after the operation
	branchInfo, _, err := p.client.Branches().GetBranch(pid, branch, gitlab.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("failed to get branch info: %w", err)
	}
	return branchInfo.Commit.ID, nil
}

// CreatePullRequest creates a merge request (GitLab's PR equivalent).
func (p *GitLabPublisher) CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) (string, error) {
	pid := projectID(owner, repo)

	opts := &gitlab.CreateMergeRequestOptions{
		Title:        &title,
		Description:  &body,
		SourceBranch: &head,
		TargetBranch: &base,
	}

	mr, _, err := p.client.MergeRequests().CreateMergeRequest(pid, opts, gitlab.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("failed to create merge request: %w", err)
	}

	return mr.WebURL, nil
}

// CreateBranch creates a new branch from a base SHA.
func (p *GitLabPublisher) CreateBranch(ctx context.Context, owner, repo, branchName, baseSHA string) error {
	pid := projectID(owner, repo)

	opts := &gitlab.CreateBranchOptions{
		Branch: &branchName,
		Ref:    &baseSHA,
	}

	_, _, err := p.client.Branches().CreateBranch(pid, opts, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	return nil
}

// GetFileSHA gets the blob SHA of an existing file.
func (p *GitLabPublisher) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	pid := projectID(owner, repo)

	opts := &gitlab.GetFileOptions{
		Ref: &ref,
	}

	file, resp, err := p.client.RepositoryFiles().GetFile(pid, path, opts, gitlab.WithContext(ctx))
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return "", nil // File doesn't exist
		}
		return "", fmt.Errorf("failed to get file: %w", err)
	}

	return file.BlobID, nil
}
