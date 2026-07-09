package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// initRepo creates a temp git repo with one commit and returns its path. It sets
// a local identity + a default branch so `git commit` succeeds under any global
// config. Callers guard on git availability before calling.
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
	require.NoError(t, writeFile(filepath.Join(dir, "README.md"), "seed"))
	runInit("add", "README.md")
	runInit("commit", "-m", "seed")
	return dir
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
