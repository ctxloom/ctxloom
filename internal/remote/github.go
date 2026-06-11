package remote

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/google/go-github/v60/github"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// GitHubFetcher implements Fetcher for GitHub repositories.
type GitHubFetcher struct {
	client   GitHubClient
	token    string       // stored token for retry logic
	hasToken bool         // whether we're using authenticated access
	fallback GitHubClient // unauthenticated client for 401 retry
}

// GitHubFetcherOption configures a GitHubFetcher.
type GitHubFetcherOption func(*gitHubFetcherConfig)

type gitHubFetcherConfig struct {
	httpClient *http.Client
	apiURL     string
}

// WithHTTPClient sets a custom HTTP client (for testing).
func WithHTTPClient(client *http.Client) GitHubFetcherOption {
	return func(c *gitHubFetcherConfig) {
		c.httpClient = client
	}
}

// WithGitHubAPIURL points the fetcher at a GitHub Enterprise REST API base
// (e.g. https://github.mycorp.com/api/v3). Empty uses the public github.com
// endpoint.
func WithGitHubAPIURL(apiURL string) GitHubFetcherOption {
	return func(c *gitHubFetcherConfig) {
		c.apiURL = apiURL
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
			Transport: &loggingTransport{base: &tokenTransport{token: token}},
		}
		hasToken = true
	} else {
		httpClient = &http.Client{
			Transport: &loggingTransport{},
		}
	}

	fetcher := &GitHubFetcher{
		client:   newRealGitHubClient(httpClient, cfg.apiURL),
		token:    token,
		hasToken: hasToken,
	}

	// Create unauthenticated fallback client for 401 retry
	if hasToken {
		fetcher.fallback = newRealGitHubClient(&http.Client{
			Transport: &loggingTransport{},
		}, cfg.apiURL)
	}

	return fetcher
}

// NewGitHubFetcherWithClient creates a GitHubFetcher with a custom client (for testing).
func NewGitHubFetcherWithClient(client GitHubClient) *GitHubFetcher {
	return &GitHubFetcher{client: client}
}

// loggingTransport logs every HTTP request to stderr for diagnostics.
// Quiet by default; set CTXLOOM_DEBUG_HTTP=1 to enable. The cached-clone path
// has eliminated most API traffic, so unconditional logging became noise on
// every legitimate operation (discover, publish).
//
// CTXLOOM_DEBUG_HTTP is the canonical switch for all HTTP debugging in ctxloom.
// It only instruments the GitHub client today, but any HTTP transport added
// later should honor this same variable rather than introduce a parallel one.
type loggingTransport struct {
	base http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	debug := os.Getenv("CTXLOOM_DEBUG_HTTP") == "1"
	if debug {
		fmt.Fprintf(os.Stderr, "ctxloom: GitHub API call: %s %s\n", req.Method, req.URL.String())
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if debug {
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: GitHub API error: %v\n", err)
		} else if resp.StatusCode >= 400 {
			fmt.Fprintf(os.Stderr, "ctxloom: GitHub API status: %d for %s\n", resp.StatusCode, req.URL.Path)
		}
	}
	return resp, err
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
	// Fall back to the typed go-github error rather than matching "401" in
	// the message text.
	var gerr *github.ErrorResponse
	if errors.As(err, &gerr) && gerr.Response != nil && gerr.Response.StatusCode == http.StatusUnauthorized {
		return true
	}
	return false
}

// shouldRetry401 checks if a 401 error occurred and we have a fallback client.
// If so, it prints a warning and returns true.
func (f *GitHubFetcher) shouldRetry401(resp *github.Response, err error) bool {
	if is401Error(resp, err) && f.fallback != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: GitHub token invalid, retrying without authentication\n")
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
		if f.shouldRetry401(resp, err) {
			content, _, resp, err = f.fallback.Repositories().GetContents(ctx, owner, repo, path, opts)
		}
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("file not found: %s/%s/%s: %w", owner, repo, path, errs.ErrRemoteContentNotFound)
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
		if f.shouldRetry401(resp, err) {
			_, dirContents, resp, err = f.fallback.Repositories().GetContents(ctx, owner, repo, path, opts)
		}
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("directory not found: %s/%s/%s: %w", owner, repo, path, errs.ErrRemoteContentNotFound)
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

// resolveRefWithClient resolves a ref to a commit SHA by trying it as a commit,
// then a branch, then a tag. Each strategy reports found=true when it resolved
// the ref (or hit a definitive error / a 401 that triggered a fallback-client
// retry); only a non-retryable miss falls through to the next strategy.
func (f *GitHubFetcher) resolveRefWithClient(ctx context.Context, client GitHubClient, owner, repo, ref string, allowRetry bool) (string, error) {
	if sha, found, err := f.resolveAsCommit(ctx, client, owner, repo, ref, allowRetry); found {
		return sha, err
	}

	sha, found, branchResp, err := f.resolveAsBranch(ctx, client, owner, repo, ref, allowRetry)
	if found {
		return sha, err
	}

	if sha, found, err := f.resolveAsTag(ctx, client, owner, repo, ref, allowRetry, branchResp); found {
		return sha, err
	}

	return "", fmt.Errorf("ref not found: %s: %w", ref, errs.ErrRemoteContentNotFound)
}

// retryRefWithFallback retries the resolution against the fallback client when
// the response is a retryable 401. Returns retried=true (with the fallback's
// result) when it fired, retried=false to let the caller continue.
func (f *GitHubFetcher) retryRefWithFallback(ctx context.Context, owner, repo, ref string, allowRetry bool, resp *github.Response, err error) (string, bool, error) {
	if !allowRetry || !f.shouldRetry401(resp, err) {
		return "", false, nil
	}
	sha, rerr := f.resolveRefWithClient(ctx, f.fallback, owner, repo, ref, false)
	return sha, true, rerr
}

// resolveAsCommit tries ref as a commit SHA (only when it looks like one).
func (f *GitHubFetcher) resolveAsCommit(ctx context.Context, client GitHubClient, owner, repo, ref string, allowRetry bool) (sha string, found bool, err error) {
	if len(ref) < 7 || len(ref) > 40 {
		return "", false, nil
	}
	commit, resp, err := client.Repositories().GetCommit(ctx, owner, repo, ref, nil)
	if err == nil {
		return commit.GetSHA(), true, nil
	}
	return f.retryRefWithFallback(ctx, owner, repo, ref, allowRetry, resp, err)
}

// resolveAsBranch tries ref as a branch name, returning the branch response so
// the tag strategy can gate on a 404.
func (f *GitHubFetcher) resolveAsBranch(ctx context.Context, client GitHubClient, owner, repo, ref string, allowRetry bool) (sha string, found bool, resp *github.Response, err error) {
	branch, resp, err := client.Repositories().GetBranch(ctx, owner, repo, ref, 0)
	if err == nil {
		return branch.GetCommit().GetSHA(), true, resp, nil
	}
	sha, found, rerr := f.retryRefWithFallback(ctx, owner, repo, ref, allowRetry, resp, err)
	return sha, found, resp, rerr
}

// resolveAsTag tries ref as a tag, but only when the branch lookup 404'd.
func (f *GitHubFetcher) resolveAsTag(ctx context.Context, client GitHubClient, owner, repo, ref string, allowRetry bool, branchResp *github.Response) (sha string, found bool, err error) {
	if branchResp == nil || branchResp.StatusCode != http.StatusNotFound {
		return "", false, nil
	}
	tagRef, tagResp, err := client.Git().GetRef(ctx, owner, repo, "tags/"+ref)
	if err == nil {
		sha, terr := f.resolveTagObjectSHA(ctx, client, owner, repo, tagRef)
		return sha, true, terr
	}
	return f.retryRefWithFallback(ctx, owner, repo, ref, allowRetry, tagResp, err)
}

// ResolveTag resolves a tag name to its commit SHA through the tag namespace
// ONLY (refs/tags/<tag>), dereferencing annotated tags. SECURITY: see
// GitCloneFetcher.ResolveTag — callers that know the ref is a tag must not go
// through the generic ResolveRef strategy order, where a same-named branch
// would win.
func (f *GitHubFetcher) ResolveTag(ctx context.Context, owner, repo, tag string) (string, error) {
	client := f.client
	tagRef, resp, err := client.Git().GetRef(ctx, owner, repo, "tags/"+tag)
	if err != nil && f.shouldRetry401(resp, err) {
		client = f.fallback
		tagRef, _, err = client.Git().GetRef(ctx, owner, repo, "tags/"+tag)
	}
	if err != nil {
		return "", fmt.Errorf("tag not found: %s: %w", tag, errs.ErrRemoteContentNotFound)
	}
	return f.resolveTagObjectSHA(ctx, client, owner, repo, tagRef)
}

// resolveTagObjectSHA returns the commit SHA a tag points to, dereferencing an
// annotated tag object (lightweight tags point straight at the commit).
func (f *GitHubFetcher) resolveTagObjectSHA(ctx context.Context, client GitHubClient, owner, repo string, tagRef *github.Reference) (string, error) {
	if tagRef.GetObject().GetType() != "tag" {
		return tagRef.GetObject().GetSHA(), nil
	}
	tag, _, err := client.Git().GetTag(ctx, owner, repo, tagRef.GetObject().GetSHA())
	if err != nil {
		return "", fmt.Errorf("failed to resolve annotated tag: %w", err)
	}
	return tag.GetObject().GetSHA(), nil
}

// SearchRepos finds ctxloom repositories.
func (f *GitHubFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	if limit <= 0 {
		limit = 30
	}

	// Search for repos named "ctxloom" or starting with "ctxloom-"
	searchQuery := "ctxloom in:name"
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
		if f.shouldRetry401(resp, err) {
			result, _, err = f.fallback.Search().Repositories(ctx, searchQuery, opts)
		}
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	}

	repos := make([]RepoInfo, 0, len(result.Repositories))
	for _, r := range result.Repositories {
		name := r.GetName()
		// Filter to only repos named "ctxloom" or "ctxloom-*"
		if name != "ctxloom" && !strings.HasPrefix(name, "ctxloom-") {
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

// ValidateRepo checks if a repository has valid ctxloom structure.
func (f *GitHubFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	// Check for the ctxloom/ directory
	_, _, resp, err := f.client.Repositories().GetContents(ctx, owner, repo, "ctxloom", nil)
	if err != nil {
		if f.shouldRetry401(resp, err) {
			_, _, resp, err = f.fallback.Repositories().GetContents(ctx, owner, repo, "ctxloom", nil)
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
		if f.shouldRetry401(resp, err) {
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
		client: newRealGitHubClient(httpClient, ""),
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
