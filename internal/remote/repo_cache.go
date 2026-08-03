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
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// cloneDirLocks serializes clone-cache mutations per repository directory. It
// is process-global (not per-RepoCache) because concurrent callers routinely
// hold independent RepoCache instances rooted at the same baseDir (e.g. the
// bundle- and profile-side prewarm goroutines, or two remotes sharing a repo
// URL): an unsynchronized RemoveAll+clone race corrupts the directory
// ("directory not empty"). Keyed by the clone path.
var cloneDirLocks sync.Map // map[string]*sync.Mutex

// lockCloneDir locks the per-directory mutex for repoDir and returns the
// unlock func.
func lockCloneDir(repoDir string) func() {
	m, _ := cloneDirLocks.LoadOrStore(repoDir, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

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
// clone at `remote create` and the init-time clone of all remotes.
func (c *RepoCache) EnsureFullRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	return c.ensureClone(ctx, repoURL, forgeType)
}

// UpdateRepo fetches the latest changes for a cached repo, advancing the
// remote-tracking refs (refs/remotes/origin/*) and tags to the live remote.
// If the repo is not yet cloned, it clones it.
func (c *RepoCache) UpdateRepo(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir, err := c.RepoDirForURL(repoURL)
	if err != nil {
		return "", fmt.Errorf("refusing to update %q: %w", repoURL, err)
	}
	unlock := lockCloneDir(repoDir)
	defer unlock()

	if !isGitRepo(repoDir) {
		return c.ensureCloneLocked(ctx, repoURL, repoDir, forgeType)
	}

	if err := c.fetch(ctx, repoDir, repoURL, forgeType); err != nil {
		// Return ("", err) on failure, matching ensureClone's
		// contract — a caller that used the returned path on error would
		// otherwise silently keep working against a (possibly now stale)
		// clone instead of treating the update as failed.
		return "", fmt.Errorf("git fetch failed: %w", err)
	}
	return repoDir, nil
}

// ensureClone guarantees a usable full clone at the cache path. A missing or
// corrupt directory is removed and freshly cloned; an existing clone is left
// as-is (callers refresh explicitly via UpdateRepo). Concurrent calls for the
// same directory serialize on the per-directory lock regardless of which
// RepoCache instance they go through.
func (c *RepoCache) ensureClone(ctx context.Context, repoURL string, forgeType ForgeType) (string, error) {
	repoDir, err := c.RepoDirForURL(repoURL)
	if err != nil {
		return "", fmt.Errorf("refusing to clone %q: %w", repoURL, err)
	}
	unlock := lockCloneDir(repoDir)
	defer unlock()
	return c.ensureCloneLocked(ctx, repoURL, repoDir, forgeType)
}

// ensureCloneLocked is ensureClone's body; the caller must hold repoDir's
// per-directory lock.
func (c *RepoCache) ensureCloneLocked(ctx context.Context, repoURL, repoDir string, forgeType ForgeType) (string, error) {
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
		if rmErr := os.RemoveAll(repoDir); rmErr != nil && !os.IsNotExist(rmErr) {
			// A failed post-clone cleanup used to be discarded here,
			// leaving exactly the corrupt/partial directory this function's
			// doc promises to remove — every later call would then trust it.
			return "", errors.Join(err, fmt.Errorf("additionally failed to clean up the partial clone at %s: %w", repoDir, rmErr))
		}
		return "", err
	}
	return repoDir, nil
}

// clone performs a full-depth clone (complete history, all branches and tags)
// via the system git binary, so the result is fully readable by go-git.
func (c *RepoCache) clone(ctx context.Context, repoURL, repoDir string, forgeType ForgeType) error {
	cloneURL := normalizeCloneURL(repoURL)
	// `--` stops git from treating a leading-dash URL as an option (argument
	// injection); the auth header rides in the environment (authEnv), not argv,
	// so the token is never exposed via /proc/<pid>/cmdline or `ps`.
	if err := runGit(ctx, "", "clone", c.authEnv(cloneURL, forgeType),
		"clone", "--", cloneURL, repoDir); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

// fetch advances all remote-tracking branches and tags to the live remote.
func (c *RepoCache) fetch(ctx context.Context, repoDir, repoURL string, forgeType ForgeType) error {
	return runGit(ctx, repoDir, "fetch", c.authEnv(normalizeCloneURL(repoURL), forgeType),
		"fetch", "--all", "--tags", "--prune", "--force")
}

// authEnv returns GIT_CONFIG_* environment entries that inject credentials for
// the github forge: the resolved token as an HTTP Authorization header scoped to
// the clone host. The token comes from the resolved forge's token_env (default
// GITHUB_TOKEN); the generic git forge gets nothing — auth is fully ambient
// (credential helper, ssh-agent, ~/.ssh/config, per-host .gitconfig).
//
// The credential is delivered via the environment (GIT_CONFIG_COUNT/KEY/VALUE,
// git >= 2.31) rather than `git -c …` on the command line, because process argv
// really is world-readable (/proc/<pid>/cmdline, `ps auxww`) and would publish
// the base64 token to every other user on the box. That part is a genuine win.
//
// It is NOT isolation, and this comment used to claim it was: "/proc/<pid>/environ
// is owner-readable only" is true (mode 0400) and irrelevant to the threat model
// that applies here. ctxloom runs agents, MCP servers and their dependencies as
// THIS user, and any same-user process can read this env. The environment is also
// INHERITED by everything git spawns — credential helpers, hooks, filters — which
// argv is not.
//
// So the honest guarantee is narrower than it looks: env-vs-argv moves the token
// from "readable by any local user" to "readable by any same-user process and any
// git child". Do not cite owner-readability as a reason to widen what rides here.
func (c *RepoCache) authEnv(cloneURL string, forgeType ForgeType) []string {
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
	return []string{
		"GIT_CONFIG_COUNT=1",
		fmt.Sprintf("GIT_CONFIG_KEY_0=http.https://%s/.extraheader", host),
		fmt.Sprintf("GIT_CONFIG_VALUE_0=AUTHORIZATION: basic %s", basic),
	}
}

// cloneToken resolves the github token for a clone URL: the resolved forge's
// token_env value when a forge resolver is configured, else the ambient token.
//
// It is also where a credential is matched to a destination, because this is
// the layer that knows the destination. The AMBIENT github credential —
// GITHUB_TOKEN/GH_TOKEN via AuthConfig.GitHub, and the same variable arriving
// as the per-type token_env default — is a github.com credential and is only
// sent to github.com. Nothing upstream asks which host it is: resolvedFromConfig
// applies the GITHUB_TOKEN default to every github-TYPED forge regardless of
// base_url, and ResolvedForge.Token falls through to AuthConfig.GitHub when the
// named variable is unset. So an ordinary
//
//	forges: {corp: {type: github, base_url: https://github.corp.example}}
//
// used to put the github.com personal access token on the wire to
// github.corp.example on every clone and fetch, as an Authorization header
// (see authEnv). No attack required.
//
// A token_env the user NAMED is a different credential and is not scoped: it
// travels to whatever host its forge points at, which is the whole point of
// configuring one. Only the unnamed, inherited value is confined.
func (c *RepoCache) cloneToken(cloneURL string) string {
	if isGitHubDotCom(cloneURL) {
		if c.resolver != nil {
			return c.resolver(cloneURL).Token(c.auth)
		}
		return c.auth.GitHub
	}

	// Off github.com only an explicitly named token_env is spendable, and it
	// is read directly rather than through Token — Token's ambient fallback is
	// exactly what must not apply here, and a named variable that is unset
	// means "no credential for this host", not "use the github.com one".
	if c.resolver == nil {
		return ""
	}
	rf := c.resolver(cloneURL)
	if rf.Type != ForgeGitHub || rf.TokenEnv == "" || rf.TokenEnv == DefaultGitHubTokenEnv {
		return ""
	}
	return os.Getenv(rf.TokenEnv)
}

// isGitHubDotCom reports whether a clone URL addresses github.com itself, the
// one host the ambient github credential belongs to. It reuses forgeHost so
// the comparison is on the parsed hostname: case, a port, and a www. prefix
// must not decide whether a credential is spent.
func isGitHubDotCom(cloneURL string) bool {
	return forgeHost(cloneURL) == "github.com"
}

// runGit invokes the system git binary, capturing stderr into any error and
// respecting ctx cancellation. dir is the working directory (empty for clone).
// label names the logical subcommand for error messages: args[0] is unreliable
// because nothing here injects credentials into argv, but a future `-c …` prefix
// would otherwise be reported as the subcommand (and could leak a token). extraEnv,
// when non-empty, is appended to the inherited environment (see authEnv) so the
// auth header is passed out of band.
//
// Every invocation is forced non-interactive: no human is necessarily present to
// answer a credential prompt (this runs headless in an agent process as often as
// at a terminal), so a private/renamed/deleted/typo'd URL must fail fast rather
// than block forever or pop a GUI dialog. GIT_TERMINAL_PROMPT=0 stops git itself
// from prompting at a terminal; GIT_ASKPASS/SSH_ASKPASS are cleared so no
// configured askpass helper runs either (an askpass is what escalates a failed
// auth into a blocking GUI dialog with nothing to answer it). This is set
// unconditionally — cmd.Env is never left nil — so the child can never silently
// inherit an interactive setup from the parent environment.
//
// This does NOT weaken legitimate auth: credential helpers (credential.helper,
// gh, git-credential-*) and SSH keys/ssh-agent authenticate without ever
// prompting a human, so GIT_TERMINAL_PROMPT and askpass — both purely about
// asking a HUMAN at a terminal/GUI — never touch that path.
func runGit(ctx context.Context, dir, label string, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// os.Environ() first, then the non-interactive overrides, then extraEnv:
	// exec.Cmd keeps only the LAST value for a duplicate key, so listing the
	// overrides after the inherited environment guarantees they win even if the
	// parent process (or its shell profile) already set GIT_ASKPASS/SSH_ASKPASS.
	//
	// GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE/GIT_COMMON_DIR override
	// cmd.Dir completely if inherited — stripped here for the same reason
	// internal/git's outputStdin strips them, so this package's clone/fetch
	// can never be silently redirected to a different repository.
	cmd.Env = append(gitutil.SanitizedEnviron(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("git binary not found on PATH (required for remote clone/fetch): %w", err)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("git %s: %w", label, ctx.Err())
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git %s: %s: %w", label, msg, err)
		}
		return fmt.Errorf("git %s: %w", label, err)
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

// RepoDirForURL computes the local cache path for a repo URL.
// e.g., https://github.com/owner/repo → baseDir/github.com/owner/repo
//
// It is the FILESYSTEM renderer over the shared ParseRepoURL grammar
// (RepoURL.CacheSegments). It used to re-parse normalizeCloneURL's output with
// url.Parse and fall back to sanitizePath on error — which is why the scp form
// keyed a "git/" user segment into the path, and why "git@host:owner/repo" and
// "git@host:owner/repo.git" cached the SAME repository in two directories.
//
// A degenerate/pathless URL (empty, ".", a bare scheme like
// "https://") must not silently resolve to the cache root itself — see
// safeRepoPath. Callers that used to discard this error and clone/RemoveAll
// whatever came back must now handle it explicitly.
func (c *RepoCache) RepoDirForURL(repoURL string) (string, error) {
	parsed, err := ParseRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	segs, err := parsed.CacheSegments()
	if err != nil {
		return "", err
	}
	return c.safeRepoPath(segs...)
}

// safeRepoPath joins parts under baseDir, guaranteeing the result stays
// STRICTLY INSIDE baseDir. SECURITY: the returned dir is later
// os.RemoveAll'd and cloned into — so "escapes baseDir" is not the only
// unsafe outcome; "equals baseDir itself" is exactly as dangerous, because
// the caller does not distinguish "a repo's own subdirectory" from "the
// entire clone cache root" before deleting it. A degenerate URL (empty,
// ".", "..", a bare scheme with no host/path) used to drop every segment and
// fall back to returning c.baseDir as though it were a safe, contained
// answer — which let RemoveAll(baseDir) wipe every cached clone.
//
// We drop empty, ".", and ".." segments before joining (so filepath.Join
// can't collapse a "../" back out), then require at least one surviving
// segment AND verify containment with filepath.Rel as defense in depth. Any
// failure returns an error — never a silent fallback to baseDir.
func (c *RepoCache) safeRepoPath(parts ...string) (string, error) {
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
	if len(clean) == 0 {
		return "", fmt.Errorf("repo path %q resolves to no usable path segments (would be the cache root itself)", strings.Join(parts, "/"))
	}
	joined := filepath.Join(append([]string{c.baseDir}, clean...)...)
	rel, err := filepath.Rel(c.baseDir, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repo path %q does not resolve to a path safely contained inside the cache root", strings.Join(parts, "/"))
	}
	return joined, nil
}

// normalizeCloneURL renders a repository URL's TRANSPORT form: the argument
// `git clone` and `git fetch` receive.
//
// It is now the CloneArg renderer over the shared ParseRepoURL grammar. It
// used to carry its own copy of the shorthand arm, guarded differently from
// NormalizeURL's — that drift is why the two were consolidated onto one
// grammar.
func normalizeCloneURL(repoURL string) string {
	parsed, err := ParseRepoURL(repoURL)
	if err != nil {
		return ""
	}
	return parsed.CloneArg()
}
