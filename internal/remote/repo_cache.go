package remote

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoCache manages local git clone caches of remote repositories.
// Instead of making per-file API calls, repos are cloned locally and
// read from the filesystem. Network operations (clone, fetch) shell out to
// the system git binary; local reads stay on go-git (git_clone_fetcher.go).
type RepoCache struct {
	baseDir  string
	auth     AuthConfig
	resolver func(repoURL string) ResolvedForge
}

// RepoCacheOption configures a RepoCache.
type RepoCacheOption func(*RepoCache)

// WithForgeResolver supplies the per-URL forge resolution used to pick the
// token for github clone auth-injection. When set, the configured forge's
// token_env names the env var read for the clone token (falling back to the
// ambient GITHUB_TOKEN); when unset, the ambient token is used.
func WithForgeResolver(resolve func(repoURL string) ResolvedForge) RepoCacheOption {
	return func(c *RepoCache) {
		c.resolver = resolve
	}
}

// NewRepoCache creates a new RepoCache.
func NewRepoCache(baseDir string, auth AuthConfig, opts ...RepoCacheOption) *RepoCache {
	c := &RepoCache{
		baseDir: baseDir,
		auth:    auth,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// EnsureRepo ensures a full clone of the repo exists locally, returning the
// local path. If the clone is missing or corrupt it is (re)cloned with complete
// history; an existing clone is returned without fetching.
func (c *RepoCache) EnsureRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	return c.ensureClone(ctx, repoURL, forgeType)
}

// EnsureRef ensures a full clone exists. Because the clone carries complete
// history (all branches and tags), every ref is locally resolvable as soon as
// the clone is present, so ref is informational only.
func (c *RepoCache) EnsureRef(ctx context.Context, repoURL string, forgeType ForgeType, ref string) (string, error) {
	return c.ensureClone(ctx, repoURL, forgeType)
}

// EnsureFullRepo ensures a full clone exists. Identical to EnsureRepo now that
// every clone is complete; retained as the explicit entry point for the eager
// clone at `remote add` and the init-time clone of all remotes.
func (c *RepoCache) EnsureFullRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	return c.ensureClone(ctx, repoURL, forgeType)
}

// UpdateRepo fetches the latest changes for a cached repo, advancing the
// remote-tracking refs (refs/remotes/origin/*) and tags to the live remote.
// If the repo is not yet cloned, it clones it.
func (c *RepoCache) UpdateRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir := c.repoDirForURL(repoURL)

	if !isGitRepo(repoDir) {
		return c.ensureClone(ctx, repoURL, forgeType)
	}

	if err := c.fetch(ctx, repoDir, repoURL, forgeType); err != nil {
		return repoDir, fmt.Errorf("git fetch failed: %w", err)
	}
	return repoDir, nil
}

// ensureClone guarantees a usable full clone at the cache path. A missing or
// corrupt directory is removed and freshly cloned; an existing clone is left
// as-is (callers refresh explicitly via UpdateRepo).
func (c *RepoCache) ensureClone(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir := c.repoDirForURL(repoURL)

	if isGitRepo(repoDir) {
		return repoDir, nil
	}

	// No usable clone — clear any stale/corrupt dir and clone fresh.
	if rmErr := os.RemoveAll(repoDir); rmErr != nil && !os.IsNotExist(rmErr) {
		return "", fmt.Errorf("failed to clean corrupt cache: %w", rmErr)
	}
	if mkErr := os.MkdirAll(filepath.Dir(repoDir), 0755); mkErr != nil {
		return "", fmt.Errorf("failed to create cache directory: %w", mkErr)
	}

	if err := c.clone(ctx, repoURL, repoDir, forgeType); err != nil {
		_ = os.RemoveAll(repoDir)
		return "", err
	}
	return repoDir, nil
}

// clone performs a full-depth clone (complete history, all branches and tags)
// via the system git binary, so the result is fully readable by go-git.
func (c *RepoCache) clone(ctx context.Context, repoURL, repoDir string, forgeType ForgeType) error {
	cloneURL := normalizeCloneURL(repoURL)
	args := append(c.authArgs(cloneURL, forgeType), "clone", cloneURL, repoDir)
	if err := runGit(ctx, "", args...); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

// fetch advances all remote-tracking branches and tags to the live remote.
func (c *RepoCache) fetch(ctx context.Context, repoDir, repoURL string, forgeType ForgeType) error {
	args := append(c.authArgs(normalizeCloneURL(repoURL), forgeType),
		"fetch", "--all", "--tags", "--prune", "--force")
	return runGit(ctx, repoDir, args...)
}

// authArgs returns leading `git -c …` arguments that inject credentials for the
// github forge: the resolved token as an HTTP Authorization header scoped to
// the clone host. The token comes from the resolved forge's token_env (default
// GITHUB_TOKEN); the generic git forge gets nothing — auth is fully ambient
// (credential helper, ssh-agent, ~/.ssh/config, per-host .gitconfig).
func (c *RepoCache) authArgs(cloneURL string, forgeType ForgeType) []string {
	if forgeType != ForgeGitHub {
		return nil
	}
	token := c.cloneToken(cloneURL)
	if token == "" {
		return nil
	}
	host := cloneHost(cloneURL)
	if host == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	header := fmt.Sprintf("http.https://%s/.extraheader=AUTHORIZATION: basic %s", host, basic)
	return []string{"-c", header}
}

// cloneToken resolves the github token for a clone URL: the resolved forge's
// token_env value when a forge resolver is configured, else the ambient token.
func (c *RepoCache) cloneToken(cloneURL string) string {
	if c.resolver != nil {
		return c.resolver(cloneURL).Token(c.auth)
	}
	return c.auth.GitHub
}

// runGit invokes the system git binary, capturing stderr into any error and
// respecting ctx cancellation. dir is the working directory (empty for clone).
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("git binary not found on PATH (required for remote clone/fetch): %w", err)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git %s: %s: %w", args[0], msg, err)
		}
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// isGitRepo reports whether dir is an existing git working tree.
func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// cloneHost returns the lowercased host of an HTTPS clone URL, or "" if the URL
// is not HTTPS (token injection only applies to HTTPS GitHub clones).
func cloneHost(cloneURL string) string {
	u, err := url.Parse(cloneURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	return strings.ToLower(u.Host)
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

// normalizeCloneURL ensures a URL is suitable for git clone.
func normalizeCloneURL(repoURL string) string {
	// Handle shorthand (owner/repo → https://github.com/owner/repo)
	if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, "@") {
		if strings.Contains(repoURL, "/") && !strings.Contains(repoURL, ".") {
			return "https://github.com/" + repoURL
		}
	}

	// Remove .git suffix for consistency, we'll let git handle it
	repoURL = strings.TrimSuffix(repoURL, ".git")

	return repoURL
}
