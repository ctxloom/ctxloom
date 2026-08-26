package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// initRepo creates a temp git repo with one commit and returns its path. It sets
// a REPO-LOCAL identity + a default branch so `git commit` succeeds under any
// global config — including none at all. Callers guard on git availability
// before calling.
//
// The local config is what makes that promise true, and the env vars below are
// NOT a substitute: they cover only the git commands this helper runs itself.
// Production code under test (ExecGit.CommitAll) shells out with a SANITIZED
// environment, so it sees neither those vars nor the caller's, and falls back to
// global config. On a developer box ~/.gitconfig quietly supplies an identity
// and the gap is invisible; in a container or on CI there is none and the commit
// dies with "Author identity unknown". Measured 2026-08-26 — this helper's doc
// claimed the promise while only the sibling initRepoUnborn kept it.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runInit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
			"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runInit("init", "-b", "main")
	runInit("config", "user.name", "ctxloom")
	runInit("config", "user.email", "ctxloom@example.com")
	require.NoError(t, writeFile(filepath.Join(dir, "README.md"), "seed"))
	runInit("add", "README.md")
	runInit("commit", "-m", "seed")
	return dir
}

// initRepoUnborn creates a temp git repo with NO commits — HEAD points at a
// branch ref that does not exist yet ("unborn"). This is what `git init`
// leaves behind, and what a project has for the whole window between creating
// it and making its first commit.
func initRepoUnborn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// A local identity, because CommitAll runs git with a sanitized
	// environment: the test must not depend on the machine's global config.
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "ctxloom"},
		{"config", "user.email", "ctxloom@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// commit writes content to path (relative to dir) and creates a commit with
// subject, returning the commit SHA. Used by LogSince tests that need more
// than the single seed commit initRepo leaves behind.
func commit(t *testing.T, dir, path, content, subject string) string {
	t.Helper()
	full := filepath.Join(dir, path)
	require.NoError(t, writeFile(full, content))
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
			"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("add", path)
	run("commit", "-m", subject)
	return run("rev-parse", "HEAD")
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func rm(path string) error { return os.Remove(path) }

// resolvePath resolves symlinks (macOS/Linux temp dirs differ) so path equality
// holds regardless of /tmp vs /private/tmp indirection.
func resolvePath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

func containsPath(list []Worktree, path string) bool {
	want, _ := filepath.EvalSymlinks(path)
	for _, w := range list {
		got, _ := filepath.EvalSymlinks(w.Path)
		if w.Path == path || (want != "" && got == want) {
			return true
		}
	}
	return false
}
