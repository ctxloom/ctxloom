// Package gitutil provides git repository utilities.
package gitutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
)

// GetOriginURL returns the URL of the `origin` remote of the git repository
// enclosing the given path. Returns an error if the path is not in a git
// repo or if origin is not configured.
func GetOriginURL(startPath string) (string, error) {
	return GetRemoteURL(startPath, "origin")
}

// GetRemoteURL returns the URL of the named remote in the git repository
// enclosing the given path.
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
