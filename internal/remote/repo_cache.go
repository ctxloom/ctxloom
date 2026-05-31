package remote

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// RepoCache manages local git clone caches of remote repositories.
// Instead of making per-file API calls, repos are cloned locally and
// read from the filesystem. Updates are explicit via UpdateRepo.
type RepoCache struct {
	baseDir string
	auth    AuthConfig
}

// NewRepoCache creates a new RepoCache.
func NewRepoCache(baseDir string, auth AuthConfig) *RepoCache {
	return &RepoCache{
		baseDir: baseDir,
		auth:    auth,
	}
}

// EnsureRepo clones a repo if it doesn't exist locally, returning the local path.
// If the repo is already cloned, it returns immediately without fetching updates.
// Use UpdateRepo to explicitly fetch updates, or EnsureRef to make a specific ref
// available locally (unshallowing if necessary).
func (c *RepoCache) EnsureRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	return c.EnsureRef(ctx, repoURL, forgeType, "")
}

// EnsureRef ensures the clone exists and the given ref is locally resolvable.
// For empty ref (default branch), a fresh shallow clone is enough.
// For a tag or non-HEAD SHA, the clone is unshallowed on miss so all history is
// available. No-op if the clone already contains the ref.
func (c *RepoCache) EnsureRef(ctx context.Context, repoURL string, forgeType ForgeType, ref string) (string, error) {
	repoDir := c.repoDirForURL(repoURL)

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		// No existing clone or corrupt — clean up and clone fresh (shallow).
		if rmErr := os.RemoveAll(repoDir); rmErr != nil && !os.IsNotExist(rmErr) {
			return "", fmt.Errorf("failed to clean corrupt cache: %w", rmErr)
		}
		if mkErr := os.MkdirAll(filepath.Dir(repoDir), 0755); mkErr != nil {
			return "", fmt.Errorf("failed to create cache directory: %w", mkErr)
		}

		auth := c.authMethod(forgeType)
		cloneURL := normalizeCloneURL(repoURL)
		repo, err = git.PlainCloneContext(ctx, repoDir, false, &git.CloneOptions{
			URL:   cloneURL,
			Depth: 1,
			Tags:  git.AllTags,
			Auth:  auth,
		})
		if err != nil {
			_ = os.RemoveAll(repoDir)
			return "", fmt.Errorf("git clone failed: %w", err)
		}
	}

	// Empty ref → default branch only, shallow clone covers it.
	if ref == "" {
		return repoDir, nil
	}

	// Ref is already locally resolvable.
	if refExistsLocally(repo, ref) {
		return repoDir, nil
	}

	// Unshallow to make all branches/tags available, then re-check.
	if err := c.unshallowRepo(ctx, repo, forgeType); err != nil {
		return repoDir, fmt.Errorf("git fetch (unshallow) failed: %w", err)
	}
	return repoDir, nil
}

// UpdateRepo fetches the latest changes for a cached repo, advancing the
// remote-tracking refs (refs/remotes/origin/*) to the live remote HEAD.
// If the repo is not yet cloned, it clones it.
func (c *RepoCache) UpdateRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir := c.repoDirForURL(repoURL)

	// Try to open existing clone
	repo, err := git.PlainOpen(repoDir)
	if err == nil {
		// Existing clone — fetch updates. A shallow clone's Depth:1 refetch does
		// NOT advance origin/* past the original shallow boundary, so updates on
		// a shallow clone would silently no-op (the bug). Always do the full,
		// unshallowing fetch (Depth:0) so refs/remotes/origin/main tracks the
		// live remote HEAD — which is what `remote update`, `remote relock`, and
		// the lockfile outdated check all rely on.
		if fetchErr := c.unshallowRepo(ctx, repo, forgeType); fetchErr != nil {
			return repoDir, fmt.Errorf("git fetch failed: %w", fetchErr)
		}
		return repoDir, nil
	}

	// Not cloned yet — do a fresh clone
	return c.EnsureRepo(ctx, repoURL, forgeType)
}

// RepoDirForURL returns the local cache path for a repo URL (exported for operations).
func (c *RepoCache) RepoDirForURL(repoURL string) string {
	return c.repoDirForURL(repoURL)
}

// repoDirForURL computes the local cache path for a repo URL.
// e.g., https://github.com/owner/repo → baseDir/github.com/owner/repo
func (c *RepoCache) repoDirForURL(repoURL string) string {
	u, err := url.Parse(normalizeCloneURL(repoURL))
	if err != nil {
		return c.safeRepoPath(sanitizePath(repoURL))
	}

	host := u.Hostname()
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")

	return c.safeRepoPath(host, path)
}

// safeRepoPath joins parts under baseDir, guaranteeing the result stays inside
// baseDir. SECURITY: the returned dir is later RemoveAll'd and cloned into, so
// a crafted repo URL with ".." segments must not traverse out of the cache.
// We drop empty, ".", and ".." segments before joining (so filepath.Join can't
// collapse a "../" back out), then verify containment with filepath.Rel as
// defense in depth, falling back to baseDir if anything still escapes.
func (c *RepoCache) safeRepoPath(parts ...string) string {
	var clean []string
	for _, p := range parts {
		for _, seg := range strings.FieldsFunc(p, func(r rune) bool {
			return r == '/' || r == filepath.Separator
		}) {
			if seg == "" || seg == "." || seg == ".." {
				continue
			}
			clean = append(clean, seg)
		}
	}
	joined := filepath.Join(append([]string{c.baseDir}, clean...)...)
	rel, err := filepath.Rel(c.baseDir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return c.baseDir
	}
	return joined
}

// unshallowRepo deepens a shallow clone so all branches and tags are locally
// resolvable, and advances refs/remotes/origin/* to the live remote HEAD.
// Used on the first miss for a non-HEAD ref and by UpdateRepo.
func (c *RepoCache) unshallowRepo(ctx context.Context, repo *git.Repository, forgeType ForgeType) error {
	auth := c.authMethod(forgeType)
	err := repo.FetchContext(ctx, &git.FetchOptions{
		Auth:  auth,
		Depth: 0,
		Tags:  git.AllTags,
		Force: true,
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/remotes/origin/*",
			"+refs/tags/*:refs/tags/*",
		},
	})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
}

// refExistsLocally returns true if the given ref string can be resolved to a
// commit hash from local refs alone. Mirrors the resolution chain in
// GitCloneFetcher.resolveToCommitHash so callers stay consistent.
func refExistsLocally(repo *git.Repository, ref string) bool {
	if ref == "" {
		return true
	}
	candidates := []string{
		ref,
		"refs/remotes/origin/" + ref,
		"refs/tags/" + ref,
	}
	for _, c := range candidates {
		if _, err := repo.ResolveRevision(plumbing.Revision(c)); err == nil {
			return true
		}
	}
	if tagRef, err := repo.Tag(ref); err == nil && tagRef != nil {
		return true
	}
	return false
}

// authMethod returns the go-git auth method for the given forge type.
func (c *RepoCache) authMethod(forgeType ForgeType) transport.AuthMethod {
	switch forgeType {
	case ForgeGitHub:
		if c.auth.GitHub != "" {
			return &http.BasicAuth{
				Username: "x-access-token",
				Password: c.auth.GitHub,
			}
		}
	case ForgeGitLab:
		if c.auth.GitLab != "" {
			return &http.BasicAuth{
				Username: "oauth2",
				Password: c.auth.GitLab,
			}
		}
	}
	return nil
}

// normalizeCloneURL ensures a URL is suitable for git clone.
func normalizeCloneURL(repoURL string) string {
	// Handle shorthand (owner/repo → https://github.com/owner/repo)
	if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, "@") {
		if strings.Contains(repoURL, "/") && !strings.Contains(repoURL, ".") {
			return "https://github.com/" + repoURL
		}
	}

	// Remove .git suffix for consistency, we'll let go-git handle it
	repoURL = strings.TrimSuffix(repoURL, ".git")

	return repoURL
}
