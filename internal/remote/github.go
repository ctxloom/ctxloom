package remote

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/go-github/v60/github"
)

// GitHubFetcher implements Fetcher for GitHub repositories.
type GitHubFetcher struct {
	client    GitHubClient
	token     string       // stored token for retry logic
	hasToken  bool         // whether we're using authenticated access
	fallback  GitHubClient // unauthenticated client for 401 retry
}

// GitHubFetcherOption configures a GitHubFetcher.
type GitHubFetcherOption func(*gitHubFetcherConfig)

type gitHubFetcherConfig struct {
	httpClient *http.Client
}

// WithHTTPClient sets a custom HTTP client (for testing).
func WithHTTPClient(client *http.Client) GitHubFetcherOption {
	return func(c *gitHubFetcherConfig) {
		c.httpClient = client
	}
}

// NewGitHubFetcher creates a new GitHub fetcher.
// If token is empty, it will try GITHUB_TOKEN env var.
// On 401 errors, the fetcher will automatically retry without authentication
// for public repositories.
func NewGitHubFetcher(token string, opts ...GitHubFetcherOption) *GitHubFetcher {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}

	cfg := &gitHubFetcherConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var httpClient *http.Client
	hasToken := false
	if cfg.httpClient != nil {
		httpClient = cfg.httpClient
	} else if token != "" {
		httpClient = &http.Client{
			Transport: &tokenTransport{token: token},
		}
		hasToken = true
	}

	fetcher := &GitHubFetcher{
		client:   newRealGitHubClient(httpClient),
		token:    token,
		hasToken: hasToken,
	}

	// Create unauthenticated fallback client for 401 retry
	if hasToken {
		fetcher.fallback = newRealGitHubClient(nil)
	}

	return fetcher
}

// NewGitHubFetcherWithClient creates a GitHubFetcher with a custom client (for testing).
func NewGitHubFetcherWithClient(client GitHubClient) *GitHubFetcher {
	return &GitHubFetcher{client: client}
}

// tokenTransport adds authorization header to requests.
type tokenTransport struct {
	token string
	base  http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// Forge returns the forge type.
func (f *GitHubFetcher) Forge() ForgeType {
	return ForgeGitHub
}

// is401Error checks if the error is a 401 Unauthorized response.
func is401Error(resp *github.Response, err error) bool {
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return true
	}
	// Also check error message for 401
	if err != nil && strings.Contains(err.Error(), "401") {
		return true
	}
	return false
}

// FetchFile retrieves raw file content from a GitHub repository.
func (f *GitHubFetcher) FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	opts := &github.RepositoryContentGetOptions{}
	if ref != "" {
		opts.Ref = ref
	}

	content, _, resp, err := f.client.Repositories().GetContents(ctx, owner, repo, path, opts)
	if err != nil {
		// Retry without auth on 401 (bad credentials) for public repos
		if is401Error(resp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			content, _, resp, err = f.fallback.Repositories().GetContents(ctx, owner, repo, path, opts)
		}
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("file not found: %s/%s/%s", owner, repo, path)
			}
			return nil, fmt.Errorf("failed to fetch file: %w", err)
		}
	}

	if content == nil {
		return nil, fmt.Errorf("path is a directory, not a file: %s", path)
	}

	// Content is base64 encoded
	decoded, err := base64.StdEncoding.DecodeString(*content.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode file content: %w", err)
	}

	return decoded, nil
}

// ListDir lists directory contents at the specified path.
func (f *GitHubFetcher) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	opts := &github.RepositoryContentGetOptions{}
	if ref != "" {
		opts.Ref = ref
	}

	_, dirContents, resp, err := f.client.Repositories().GetContents(ctx, owner, repo, path, opts)
	if err != nil {
		// Retry without auth on 401 (bad credentials) for public repos
		if is401Error(resp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			_, dirContents, resp, err = f.fallback.Repositories().GetContents(ctx, owner, repo, path, opts)
		}
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("directory not found: %s/%s/%s", owner, repo, path)
			}
			return nil, fmt.Errorf("failed to list directory: %w", err)
		}
	}

	entries := make([]DirEntry, 0, len(dirContents))
	for _, item := range dirContents {
		entry := DirEntry{
			Name:  item.GetName(),
			IsDir: item.GetType() == "dir",
			SHA:   item.GetSHA(),
			Size:  int64(item.GetSize()),
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// ResolveRef converts a git reference to a commit SHA.
func (f *GitHubFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	return f.resolveRefWithClient(ctx, f.client, owner, repo, ref, true)
}

func (f *GitHubFetcher) resolveRefWithClient(ctx context.Context, client GitHubClient, owner, repo, ref string, allowRetry bool) (string, error) {
	// Try as a commit SHA first (if it looks like one)
	if len(ref) >= 7 && len(ref) <= 40 {
		commit, resp, err := client.Repositories().GetCommit(ctx, owner, repo, ref, nil)
		if err == nil {
			return commit.GetSHA(), nil
		}
		// Retry on 401
		if allowRetry && is401Error(resp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			return f.resolveRefWithClient(ctx, f.fallback, owner, repo, ref, false)
		}
	}

	// Try as a branch
	branch, resp, err := client.Repositories().GetBranch(ctx, owner, repo, ref, 0)
	if err == nil {
		return branch.GetCommit().GetSHA(), nil
	}
	// Retry on 401
	if allowRetry && is401Error(resp, err) && f.fallback != nil {
		fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
		return f.resolveRefWithClient(ctx, f.fallback, owner, repo, ref, false)
	}

	// Try as a tag
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		tagRef, tagResp, err := client.Git().GetRef(ctx, owner, repo, "tags/"+ref)
		if err == nil {
			// Tag might be annotated (points to tag object) or lightweight (points to commit)
			if tagRef.GetObject().GetType() == "tag" {
				// Annotated tag - get the commit it points to
				tag, _, err := client.Git().GetTag(ctx, owner, repo, tagRef.GetObject().GetSHA())
				if err != nil {
					return "", fmt.Errorf("failed to resolve annotated tag: %w", err)
				}
				return tag.GetObject().GetSHA(), nil
			}
			return tagRef.GetObject().GetSHA(), nil
		}
		// Retry on 401
		if allowRetry && is401Error(tagResp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			return f.resolveRefWithClient(ctx, f.fallback, owner, repo, ref, false)
		}
	}

	return "", fmt.Errorf("ref not found: %s", ref)
}

// SearchRepos finds SCM repositories.
func (f *GitHubFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	if limit <= 0 {
		limit = 30
	}

	// Search for repos named "scm" or starting with "scm-"
	searchQuery := "scm in:name"
	if query != "" {
		searchQuery = fmt.Sprintf("%s %s", query, searchQuery)
	}

	opts := &github.SearchOptions{
		Sort:  "stars",
		Order: "desc",
		ListOptions: github.ListOptions{
			PerPage: limit,
		},
	}

	result, resp, err := f.client.Search().Repositories(ctx, searchQuery, opts)
	if err != nil {
		// Retry without auth on 401 (bad credentials)
		if is401Error(resp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			result, _, err = f.fallback.Search().Repositories(ctx, searchQuery, opts)
		}
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	}

	repos := make([]RepoInfo, 0, len(result.Repositories))
	for _, r := range result.Repositories {
		name := r.GetName()
		// Filter to only repos named "scm" or "scm-*"
		if name != "scm" && !strings.HasPrefix(name, "scm-") {
			continue
		}

		repos = append(repos, RepoInfo{
			Owner:       r.GetOwner().GetLogin(),
			Name:        name,
			Description: r.GetDescription(),
			Stars:       r.GetStargazersCount(),
			URL:         r.GetHTMLURL(),
			Topics:      r.Topics,
			Language:    r.GetLanguage(),
			UpdatedAt:   r.GetUpdatedAt().Time,
			Forge:       ForgeGitHub,
		})
	}

	return repos, nil
}

// ValidateRepo checks if a repository has valid SCM structure.
func (f *GitHubFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	// Check for scm/v1/ directory
	_, _, resp, err := f.client.Repositories().GetContents(ctx, owner, repo, "scm/v1", nil)
	if err != nil {
		// Retry without auth on 401 (bad credentials) for public repos
		if is401Error(resp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			_, _, resp, err = f.fallback.Repositories().GetContents(ctx, owner, repo, "scm/v1", nil)
		}
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return false, nil
			}
			return false, fmt.Errorf("failed to check repo structure: %w", err)
		}
	}
	return true, nil
}

// GetDefaultBranch returns the default branch name.
func (f *GitHubFetcher) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, resp, err := f.client.Repositories().Get(ctx, owner, repo)
	if err != nil {
		// Retry without auth on 401 (bad credentials) for public repos
		if is401Error(resp, err) && f.fallback != nil {
			fmt.Fprintf(os.Stderr, "SCM: GitHub token invalid, retrying without authentication\n")
			r, _, err = f.fallback.Repositories().Get(ctx, owner, repo)
		}
		if err != nil {
			return "", fmt.Errorf("failed to get repo info: %w", err)
		}
	}
	return r.GetDefaultBranch(), nil
}

// GitHubPublisher implements Publisher for GitHub repositories.
type GitHubPublisher struct {
	client GitHubClient
}

// NewGitHubPublisher creates a new GitHub publisher.
func NewGitHubPublisher(token string) *GitHubPublisher {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}

	var httpClient *http.Client
	if token != "" {
		httpClient = &http.Client{
			Transport: &tokenTransport{token: token},
		}
	}

	return &GitHubPublisher{
		client: newRealGitHubClient(httpClient),
	}
}

// NewGitHubPublisherWithClient creates a GitHubPublisher with a custom client (for testing).
func NewGitHubPublisherWithClient(client GitHubClient) *GitHubPublisher {
	return &GitHubPublisher{client: client}
}

// CreateOrUpdateFile creates or updates a file in a repository.
func (p *GitHubPublisher) CreateOrUpdateFile(ctx context.Context, owner, repo, path, branch, message string, content []byte) (string, error) {
	// Check if file exists to get its SHA
	existingSHA, _ := p.GetFileSHA(ctx, owner, repo, path, branch)

	opts := &github.RepositoryContentFileOptions{
		Message: github.String(message),
		Content: content,
		Branch:  github.String(branch),
	}

	if existingSHA != "" {
		opts.SHA = github.String(existingSHA)
	}

	result, _, err := p.client.Repositories().CreateFile(ctx, owner, repo, path, opts)
	if err != nil {
		return "", fmt.Errorf("failed to create/update file: %w", err)
	}

	return result.GetSHA(), nil
}

// CreatePullRequest creates a pull request.
func (p *GitHubPublisher) CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) (string, error) {
	pr, _, err := p.client.PullRequests().Create(ctx, owner, repo, &github.NewPullRequest{
		Title: github.String(title),
		Body:  github.String(body),
		Head:  github.String(head),
		Base:  github.String(base),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create pull request: %w", err)
	}

	return pr.GetHTMLURL(), nil
}

// CreateBranch creates a new branch from a base SHA.
func (p *GitHubPublisher) CreateBranch(ctx context.Context, owner, repo, branchName, baseSHA string) error {
	ref := &github.Reference{
		Ref: github.String("refs/heads/" + branchName),
		Object: &github.GitObject{
			SHA: github.String(baseSHA),
		},
	}

	_, _, err := p.client.Git().CreateRef(ctx, owner, repo, ref)
	if err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	return nil
}

// GetFileSHA gets the blob SHA of an existing file.
func (p *GitHubPublisher) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	opts := &github.RepositoryContentGetOptions{}
	if ref != "" {
		opts.Ref = ref
	}

	content, _, resp, err := p.client.Repositories().GetContents(ctx, owner, repo, path, opts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return "", nil // File doesn't exist
		}
		return "", fmt.Errorf("failed to get file: %w", err)
	}

	if content == nil {
		return "", fmt.Errorf("path is a directory: %s", path)
	}

	return content.GetSHA(), nil
}
