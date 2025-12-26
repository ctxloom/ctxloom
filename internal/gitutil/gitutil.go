// Package gitutil provides git operations using go-git.
package gitutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// CloneOptions configures a clone operation.
type CloneOptions struct {
	// Depth limits history depth. 0 means full history.
	Depth int
	// Branch to checkout. Empty means default branch.
	Branch string
}

// Clone clones a git repository to the specified directory.
func Clone(url, destDir string, opts *CloneOptions) error {
	if opts == nil {
		opts = &CloneOptions{}
	}

	cloneOpts := &git.CloneOptions{
		URL: url,
	}

	if opts.Depth > 0 {
		cloneOpts.Depth = opts.Depth
	}

	if opts.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(opts.Branch)
		cloneOpts.SingleBranch = true
	}

	_, err := git.PlainClone(destDir, false, cloneOpts)
	if err != nil {
		return fmt.Errorf("clone %s: %w", url, err)
	}

	return nil
}

// ShallowClone clones a repository with depth 1.
func ShallowClone(url, destDir string) error {
	return Clone(url, destDir, &CloneOptions{Depth: 1})
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

// IsRepo checks if the given path is inside a git repository.
func IsRepo(path string) bool {
	_, err := FindRoot(path)
	return err == nil
}

// Repo wraps a git repository for convenient operations.
type Repo struct {
	repo *git.Repository
}

// Open opens an existing git repository at the given path.
// It will search up the directory tree to find the .git directory.
func Open(path string) (*Repo, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}

	repo, err := git.PlainOpenWithOptions(absPath, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}

	return &Repo{repo: repo}, nil
}

// Branch returns the current branch name.
// Returns "HEAD" if in detached HEAD state.
func (r *Repo) Branch() (string, error) {
	head, err := r.repo.Head()
	if err != nil {
		return "", fmt.Errorf("get HEAD: %w", err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	// Detached HEAD
	return "HEAD", nil
}

// Commit represents a git commit.
type Commit struct {
	Hash    string
	Message string
}

// RecentCommits returns the n most recent commits.
func (r *Repo) RecentCommits(n int) ([]Commit, error) {
	iter, err := r.repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("get log: %w", err)
	}
	defer iter.Close()

	var commits []Commit
	for i := 0; i < n; i++ {
		c, err := iter.Next()
		if err != nil {
			break // No more commits
		}
		// Get first line of commit message
		msg := c.Message
		if idx := indexOf(msg, '\n'); idx != -1 {
			msg = msg[:idx]
		}
		commits = append(commits, Commit{
			Hash:    c.Hash.String()[:7],
			Message: msg,
		})
	}

	return commits, nil
}

// RemoteURL returns the URL for the named remote (e.g., "origin").
// Returns empty string if remote doesn't exist.
func (r *Repo) RemoteURL(name string) string {
	remote, err := r.repo.Remote(name)
	if err != nil {
		return ""
	}

	urls := remote.Config().URLs
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}

// FileStatus represents the status of a file in the working tree.
type FileStatus struct {
	Path       string
	Staging    rune // Status in staging area: ' ', 'M', 'A', 'D', 'R', 'C', '?'
	Worktree   rune // Status in worktree: ' ', 'M', 'D', '?'
}

// Status returns the working tree status.
func (r *Repo) Status() ([]FileStatus, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("get worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}

	var files []FileStatus
	for path, s := range status {
		files = append(files, FileStatus{
			Path:     path,
			Staging:  rune(s.Staging),
			Worktree: rune(s.Worktree),
		})
	}

	return files, nil
}

// StatusShort returns status in git's short format (e.g., "M  file.go").
func (r *Repo) StatusShort() (string, error) {
	files, err := r.Status()
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "", nil
	}

	var lines []string
	for _, f := range files {
		staging := f.Staging
		worktree := f.Worktree
		if staging == 0 {
			staging = ' '
		}
		if worktree == 0 {
			worktree = ' '
		}
		lines = append(lines, fmt.Sprintf("%c%c %s", staging, worktree, f.Path))
	}

	return joinLines(lines), nil
}

// Root returns the repository root directory.
func (r *Repo) Root() (string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}
	return wt.Filesystem.Root(), nil
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	result := lines[0]
	for i := 1; i < len(lines); i++ {
		result += "\n" + lines[i]
	}
	return result
}
