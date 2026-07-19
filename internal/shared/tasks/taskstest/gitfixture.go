package taskstest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RealGitWorktreeFixture creates a real git repo (main, with one commit) and a
// LINKED worktree of it (linked) via `git worktree add`, each under its own
// fresh t.TempDir(). Skips the test if git isn't on PATH. Returns both roots
// as absolute, symlink-resolved paths so they compare equal to whatever git
// itself resolved into the gitdir pointer.
//
// This is the CANONICAL body for every package that needs a REAL linked
// worktree to exercise worktree-detection/redirect logic against
// (internal/projectroot, internal/taskloom/workdir, this package's own
// callers in internal/shared/tasks/operations). internal/config's
// worktree_signpost_test.go keeps its own verbatim copy rather than
// depending on this one — that file is a frozen acceptance gate (task
// brown-canal) and must stay byte-for-byte unmodified — but every OTHER
// caller should use this one instead of adding a third copy.
func RealGitWorktreeFixture(t *testing.T) (main, linked string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	mainDir := t.TempDir()
	runGit(t, mainDir, "init", "-q")
	runGit(t, mainDir, "config", "user.email", "test@example.com")
	runGit(t, mainDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(mainDir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	runGit(t, mainDir, "add", "f.txt")
	runGit(t, mainDir, "commit", "-q", "-m", "init")

	linkedDir := filepath.Join(t.TempDir(), "linked-wt")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "wt-branch", linkedDir)

	main, err := filepath.EvalSymlinks(mainDir)
	if err != nil {
		t.Fatalf("eval symlinks main: %v", err)
	}
	linked, err = filepath.EvalSymlinks(linkedDir)
	if err != nil {
		t.Fatalf("eval symlinks linked: %v", err)
	}
	return main, linked
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
