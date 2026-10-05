// Package gitutil is the IN-PROCESS git layer: it answers questions about a
// repository by reading it with go-git, without running the git binary.
//
// WHICH LAYER TO REACH FOR. ctxloom has two, and they are not interchangeable:
//
//   - internal/adapters/git EXECUTES the git binary. Use it to DO things — commit,
//     fetch, diff, list worktrees — and for anything whose answer must match
//     what git itself would say, because it is git saying it. It inherits
//     git's own repository discovery and its config (including whatever the
//     user has configured), which is a feature and a variable.
//   - gitutil (here) resolves the repository IN PROCESS with go-git. Use it
//     for the small, hot, read-only questions on ctxloom's startup path —
//     where is the repo root, what is origin's URL — where spawning a
//     subprocess per call would be the dominant cost. It sees only what go-git
//     implements.
//
// The two do NOT resolve "the repository" by the same rules, and are not
// guaranteed to agree on the same directory. That is a real, measured
// difference, not a theoretical one: this package had to be told
// EnableDotGitCommonDir explicitly before it would resolve a linked worktree
// the way the git binary always had. Do not treat one layer's answer as a
// substitute for the other's, and do not add write operations here — a mutation
// that git and go-git disagree about is not a mutation anyone can review.
//
// ENVIRONMENT SANITIZATION. Both layers depend on this package for one thing
// in common: IsRepoLocationEnvVar and SanitizedEnviron are the single home of
// the environment variables that override WHICH repository git operates on, or
// its config. internal/adapters/git and internal/adapters/remote build every git
// child process's environment from SanitizedEnviron so that cmd.Dir is the only
// thing selecting a repository. The set is not kept by hand: it is git's own
// answer to `git rev-parse --local-env-vars`, asked once per process. That probe
// is the one subprocess this package runs, and it reads no repository — it lives
// here, rather than in the layer that executes git, because
// internal/adapters/remote needs the same set and adapters do not import each
// other.
package gitutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// SanitizedEnviron returns os.Environ() without the variables
// IsRepoLocationEnvVar names — removed outright, not set to empty (git treats
// GIT_DIR="" as still present) — so cmd.Dir and the repository's own config are
// the only things selecting which repository a git child process operates on
// and how it is configured. Shared by internal/adapters/git (the exec.Cmd-based
// git.Git implementation) and internal/adapters/remote (clone/fetch over the
// network).
func SanitizedEnviron() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !IsRepoLocationEnvVar(key) {
			out = append(out, kv)
		}
	}
	return out
}

// resolveStartDir turns a caller's starting path into the absolute DIRECTORY
// to open a repository from: a file resolves to its parent, a directory is
// used as-is.
//
// A path that cannot be stat'd is an ERROR, not something to carry on with.
// Both entry points below open the repository with DetectDotGit, which walks
// UP from whatever it is given — so a typo'd or deleted path does not fail,
// it answers from some ANCESTOR repository instead. "the remote of a path
// that does not exist" then comes back as a real URL, which is worse than no
// answer: a missing answer is visibly missing, and a wrong one is not.
//
// This is one function because it is one question. It was answered twice,
// thirty lines apart, with opposite policies.
func resolveStartDir(startPath string) (string, error) {
	absPath, err := filepath.Abs(startPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return filepath.Dir(absPath), nil
	}
	return absPath, nil
}

// GetRemoteURL returns the FETCH URL of the named remote in the git repository
// enclosing the given path. Returns an error if the path is not in a git repo
// or if the remote is not configured.
//
// A remote can carry several URLs — git fetches from the first `url` and
// pushes to all of them, or to `pushurl` when one is set — and go-git presents
// them as one slice, `url` entries first, `pushurl` entries after. The first
// element is therefore the fetch URL, and that is deliberately the one
// returned: this answers "which configured remote is this checkout", and a
// remote's identity is where it is read FROM. Returning a push URL would give
// the same remote a second name and defeat the matching this exists for.
func GetRemoteURL(startPath, remoteName string) (string, error) {
	absPath, err := resolveStartDir(startPath)
	if err != nil {
		return "", err
	}

	repo, err := git.PlainOpenWithOptions(absPath, &git.PlainOpenOptions{
		DetectDotGit: true,
		// Without this, go-git resolves a linked worktree's ".git"
		// FILE (gitdir: pointer) as the WHOLE repository — a private
		// per-worktree admin dir with no `config` (that lives only in the
		// common dir) — so `remote` always reports unconfigured there, even
		// when origin genuinely exists.
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	rem, err := repo.Remote(remoteName)
	if err != nil {
		return "", fmt.Errorf("remote %q not configured: %w", remoteName, err)
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return "", fmt.Errorf("remote %q has no URL", remoteName)
	}
	return urls[0], nil
}

// IsNoRepository reports whether err means "this path is not inside a git
// repository" — the expected, benign outcome of probing an arbitrary
// directory — as opposed to any other failure FindRoot can return: an
// unreadable or corrupt .git, a path that cannot be stat'd, a repository with
// no worktree. Both arrive as a non-nil error and callers that only check
// `err != nil` treat a real fault as an ordinary "not here", which is how a
// broken repository becomes a silent fallback. Classification lives here
// because this package owns the wrapping that hides go-git's sentinel.
func IsNoRepository(err error) bool {
	return errors.Is(err, git.ErrRepositoryNotExists)
}

// FindRoot finds the git repository root starting from the given path.
// It walks up the directory tree until it finds a .git directory.
// Returns the absolute path to the repository root, or an error if not in a git repo.
func FindRoot(startPath string) (string, error) {
	absPath, err := resolveStartDir(startPath)
	if err != nil {
		return "", err
	}

	repo, err := git.PlainOpenWithOptions(absPath, &git.PlainOpenOptions{
		DetectDotGit: true,
		// Consistent with GetRemoteURL above, even though
		// FindRoot's own result is unaffected either way (the worktree
		// filesystem root is the containing directory regardless of common-
		// dir resolution) — one word, twice, so the two literals never
		// silently diverge again.
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	return wt.Filesystem.Root(), nil
}

// DefaultShortSHALen is the abbreviation width for a commit SHA in ordinary
// output — git's own default.
const DefaultShortSHALen = 7

// ShortSHA abbreviates a commit SHA to DefaultShortSHALen characters. This is
// the width every listing, status line and lockfile report uses.
func ShortSHA(sha string) string { return AbbrevSHA(sha, DefaultShortSHALen) }

// AbbrevSHA abbreviates a commit SHA to n characters, returning it unchanged
// when it is already that short. It never slices past the end: these values
// come from lockfiles and git output, so a malformed or truncated hash must
// render, not panic. Callers that need a width other than the default (a
// prompt whose evidence lines want a longer prefix) name it here rather than
// growing another copy of the rule.
func AbbrevSHA(sha string, n int) string {
	if len(sha) > n {
		return sha[:n]
	}
	return sha
}
