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
