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
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
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
// Use UpdateRepo to explicitly fetch updates.
func (c *RepoCache) EnsureRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir := c.repoDirForURL(repoURL)

	// If already cloned, return immediately
	if _, err := git.PlainOpen(repoDir); err == nil {
		return repoDir, nil
	}

	// No existing clone or corrupt — clean up and clone fresh
	if err := os.RemoveAll(repoDir); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to clean corrupt cache: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return "", fmt.Errorf("failed to create cache directory: %w", err)
	}

	auth := c.authMethod(forgeType)
	cloneURL := normalizeCloneURL(repoURL)
	_, err := git.PlainCloneContext(ctx, repoDir, false, &git.CloneOptions{
		URL:   cloneURL,
		Depth: 1,
		Tags:  git.AllTags,
		Auth:  auth,
	})
	if err != nil {
		// Clean up partial clone
		_ = os.RemoveAll(repoDir)
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	return repoDir, nil
}

// UpdateRepo fetches the latest changes for a cached repo.
// If the repo is not yet cloned, it clones it.
func (c *RepoCache) UpdateRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir := c.repoDirForURL(repoURL)
	auth := c.authMethod(forgeType)

	// Try to open existing clone
	repo, err := git.PlainOpen(repoDir)
	if err == nil {
		// Existing clone — fetch updates
		if fetchErr := c.fetchRepo(ctx, repo, auth); fetchErr != nil {
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
		return filepath.Join(c.baseDir, sanitizePath(repoURL))
	}

	host := u.Hostname()
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")

	return filepath.Join(c.baseDir, host, path)
}

// fetchRepo fetches updates for an existing clone.
func (c *RepoCache) fetchRepo(ctx context.Context, repo *git.Repository, auth transport.AuthMethod) error {
	err := repo.FetchContext(ctx, &git.FetchOptions{
		Auth:  auth,
		Depth: 1,
		Tags:  git.AllTags,
		Force: true,
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/remotes/origin/*",
		},
	})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
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

// lsRemoteDefaultBranch uses ls-remote to determine the default branch
// without cloning the repo. This is used by GitCloneFetcher when HEAD
// is detached or unavailable.
func lsRemoteDefaultBranch(ctx context.Context, repoURL string, auth transport.AuthMethod) (string, error) {
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{repoURL},
	})

	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", fmt.Errorf("ls-remote failed: %w", err)
	}

	// Find HEAD symref target
	for _, ref := range refs {
		if ref.Name() == "HEAD" && ref.Target() != "" {
			return ref.Target().Short(), nil
		}
	}

	// Fallback: look for main or master
	for _, ref := range refs {
		name := ref.Name().Short()
		if name == "main" || name == "master" {
			return name, nil
		}
	}

	return "main", nil
}
