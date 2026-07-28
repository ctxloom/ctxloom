package remote

import (
	"context"
)

// Fetcher abstracts git forge operations (GitHub API, generic git clone).
// Each forge implementation handles authentication, rate limiting, and API specifics.
type Fetcher interface {
	// FetchFile retrieves raw file content from a repository.
	// path is relative to repository root (e.g., ".ctxloom/content/fragments/security.yaml")
	// ref is a git reference (tag, branch, or commit SHA); empty means default branch
	FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error)

	// ListDir lists directory contents at the specified path.
	// Returns entries sorted by name.
	ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error)

	// ResolveRef converts a git reference (tag/branch) to a commit SHA.
	// Returns the SHA and error if the ref doesn't exist.
	ResolveRef(ctx context.Context, owner, repo, ref string) (string, error)

	// SearchRepos finds repositories matching the naming convention.
	// Searches for repos named "ctxloom" or starting with "ctxloom-".
	// query is an optional additional search term (e.g., "golang" to filter).
	SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error)

	// ValidateRepo checks if a repository has valid ctxloom structure.
	// Returns true if ctxloom/ directory exists with fragments or prompts.
	ValidateRepo(ctx context.Context, owner, repo string) (bool, error)

	// GetDefaultBranch returns the default branch name for a repository.
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)

	// Forge returns the type of forge this fetcher handles.
	Forge() ForgeType
}
