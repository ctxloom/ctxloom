// Package gitutil provides git repository utilities.
package gitutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// RepoLocationEnvVars are environment variables that override WHICH
// repository git operates on, ignoring an exec.Cmd's Dir entirely: GIT_DIR/
// GIT_WORK_TREE point git at a different worktree outright; GIT_INDEX_FILE/
// GIT_COMMON_DIR/GIT_OBJECT_DIRECTORY redirect pieces of it. If any of these
// leak from ctxloom's own process environment into a git child process,
// every operation could silently target the wrong repository (U053-F02).
var RepoLocationEnvVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
}

// SanitizedEnviron returns os.Environ() with RepoLocationEnvVars stripped —
// removed outright, not set to empty (git treats GIT_DIR="" as still
// present) — so cmd.Dir is the only thing that can select which repository a
// git child process operates on. Shared by internal/git (the exec.Cmd-based
// git.Git implementation) and internal/remote (clone/fetch over the network)
// so the one list of dangerous env vars lives in one place.
func SanitizedEnviron() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		blocked := false
		for _, bad := range RepoLocationEnvVars {
			if key == bad {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, kv)
		}
	}
	return out
}

// GetRemoteURL returns the URL of the named remote in the git repository
// enclosing the given path. Returns an error if the path is not in a git repo
// or if the remote is not configured.
func GetRemoteURL(startPath, remoteName string) (string, error) {
	absPath, err := filepath.Abs(startPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}

	repo, err := git.PlainOpenWithOptions(absPath, &git.PlainOpenOptions{
		DetectDotGit: true,
		// U110-F01: without this, go-git resolves a linked worktree's ".git"
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
	absPath, err := filepath.Abs(startPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Check if startPath is a file, use its directory
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}

	repo, err := git.PlainOpenWithOptions(absPath, &git.PlainOpenOptions{
		DetectDotGit: true,
		// U110-F01: consistent with GetRemoteURL above, even though
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
