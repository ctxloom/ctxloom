//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SeedRemote creates a bare git repo seeded with the given files on its main
// branch and returns a file:// clone URL. It is the error-returning analogue of
// SeedGitRepo for use from godog steps (no *testing.T). The repo lives under the
// environment Root, so TestEnvironment.Cleanup removes it.
func (e *TestEnvironment) SeedRemote(files map[string]string) (string, error) {
	root, err := os.MkdirTemp(e.Root, "remote-*")
	if err != nil {
		return "", err
	}
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	for _, s := range [][]string{
		{"init", "--bare", "-b", "main", bare},
		{"init", "-b", "main", work},
	} {
		if err := runGitE("", s...); err != nil {
			return "", err
		}
	}
	for _, s := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
	} {
		if err := runGitE(work, s...); err != nil {
			return "", err
		}
	}

	for rel, content := range files {
		full := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "", err
		}
	}

	for _, s := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "seed"},
		{"remote", "add", "origin", bare},
		{"push", "origin", "main"},
	} {
		if err := runGitE(work, s...); err != nil {
			return "", err
		}
	}
	if err := runGitE(bare, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return "", err
	}
	return "file://" + bare, nil
}

// AdvanceRemote pushes a second commit to a bare repo previously seeded by
// SeedRemote, rewriting the given files. It is the error-returning analogue of
// GitRepo.CommitFile for godog steps — it produces the multi-commit state that
// `remote sync` detects and stages for review. bareDir is the bare repository
// path (the SeedRemote URL with the file:// prefix stripped).
func (e *TestEnvironment) AdvanceRemote(bareDir string, files map[string]string) error {
	work, err := os.MkdirTemp(e.Root, "advance-*")
	if err != nil {
		return err
	}
	if err := runGitE("", "clone", bareDir, work); err != nil {
		return err
	}
	for _, s := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
	} {
		if err := runGitE(work, s...); err != nil {
			return err
		}
	}
	for rel, content := range files {
		full := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for _, s := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "advance"},
		{"push", "origin", "main"},
	} {
		if err := runGitE(work, s...); err != nil {
			return err
		}
	}
	return nil
}

// UnpublishFromRemote pushes a commit to bareDir that DELETES the given
// repo-relative paths. It is the deliberate counterpart to AdvanceRemote: that
// one rewrites content, this one takes it away, which is the only way to
// produce the state a reconcile has to treat as authority — a remote that is
// perfectly reachable and genuinely no longer serves a bundle.
func (e *TestEnvironment) UnpublishFromRemote(bareDir string, paths ...string) error {
	work, err := os.MkdirTemp(e.Root, "unpublish-*")
	if err != nil {
		return err
	}
	if err := runGitE("", "clone", bareDir, work); err != nil {
		return err
	}
	for _, s := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
	} {
		if err := runGitE(work, s...); err != nil {
			return err
		}
	}
	for _, rel := range paths {
		full := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("unpublish %s: %w", rel, err)
		}
	}
	for _, s := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "unpublish"},
		{"push", "origin", "main"},
	} {
		if err := runGitE(work, s...); err != nil {
			return err
		}
	}
	return nil
}

// BreakRemote renames a bare repository out from under its file:// URL and
// returns the new path. Every subsequent clone or fetch against that URL fails
// exactly as a network partition, a revoked credential or a deleted repository
// would — an undifferentiated failure at the fetcher seam, which is precisely
// the state a reconcile must never read as "upstream deleted everything".
func (e *TestEnvironment) BreakRemote(bareDir string) (string, error) {
	broken := bareDir + ".unreachable"
	if err := os.Rename(bareDir, broken); err != nil {
		return "", fmt.Errorf("break remote %s: %w", bareDir, err)
	}
	return broken, nil
}

// FindRepoCacheClone locates the git clone the CLI cached under this
// environment's project (.ctxloom/cache/repos/...) for a previously seeded
// remote. Scenarios in this suite seed at most one remote, so the first clone
// found (a directory containing ".git") is unambiguous. Used by steps that need
// to reach into the clone directly (e.g. to force its checkout stale) rather
// than through any ctxloom-facing surface.
func (e *TestEnvironment) FindRepoCacheClone() (string, error) {
	root := filepath.Join(e.ProjectDir, ".ctxloom", "cache", "repos")
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no cached git clone found under %s", root)
	}
	return found, nil
}

// ResetCachedCloneToFirstCommit forces clone's checked-out branch and working
// tree back to the repo's very first commit (found via the remote-tracking
// history, so it works regardless of what the local branch currently points
// at). This simulates a clone whose checkout has drifted arbitrarily far behind
// the remote-tracking refs a fetch keeps current.
func (e *TestEnvironment) ResetCachedCloneToFirstCommit(clone string) error {
	root, err := gitOutput(clone, "rev-list", "--max-parents=0", "refs/remotes/origin/main")
	if err != nil {
		return fmt.Errorf("find root commit: %w", err)
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("no root commit found in %s", clone)
	}
	if err := runGitE(clone, "reset", "--hard", root); err != nil {
		return fmt.Errorf("reset clone to %s: %w", root, err)
	}
	return nil
}

// gitOutput runs git in dir and returns its trimmed stdout, wrapping any
// failure with stderr for diagnosis.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out), nil
}

func runGitE(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}
