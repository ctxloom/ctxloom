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
// callers in internal/shared/tasks/operations). Exactly two other sites build
// a linked worktree by hand, each for a reason that rules out calling this
// one, and there must be no others — gitfixture_test.go enforces the list:
//
//   - internal/config/worktree_signpost_test.go keeps a verbatim copy: it is a
//     frozen acceptance gate (task brown-canal) and must stay byte-for-byte
//     unmodified, so it cannot take on a dependency.
//   - tests/integration/testenv's TestEnvironment.AddGitWorktree is a
//     differently-shaped helper, not a copy of this body: it branches an
//     already-initialized repo, returns an error instead of failing the test,
//     and must run git through TestEnvironment's own isolated env/dir plumbing
//     so callers land in the same isolated HOME every acceptance helper trusts.
//
// Every other caller uses this one.
func RealGitWorktreeFixture(t *testing.T) (main, linked string) {
	t.Helper()
	requireGit(t)

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

// IsLinkedWorktreeRoot reports whether dir is the root of a git LINKED
// worktree. git marks those with a .git FILE holding a gitdir pointer, where a
// primary checkout gets a .git DIRECTORY — the same discriminator
// TestRealGitWorktreeFixture_BuildsARealLinkedWorktree pins. It beats matching
// on a path convention like .claude/worktrees/, which only covers the
// worktrees whichever tool is in fashion today happens to create.
//
// It is exported for the source-scanning gates that walk a CHECKOUT ROOT. A
// checkout that hosts agent worktrees carries a full second copy of the tree
// under .claude/worktrees/agent-*, at a stale commit; a walk that ingests
// those copies is reading code that is not the code under test, so it can
// false-fail on someone else's debris or — where the assertion is a floor or a
// count — false-PASS by inflating it.
//
// Such a walk must NEVER skip its own root. An agent worktree IS a linked
// worktree, and the suite routinely runs from one, so skipping the root would
// scan nothing and report a vacuous pass. Guard the call with `path != root`.
func IsLinkedWorktreeRoot(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && info.Mode().IsRegular()
}

// EnvAllowMissingGit, set to "1", downgrades "git is not on PATH" from a
// failure to a skip. It is an escape hatch, not the default: see
// requireGit.
const EnvAllowMissingGit = "CTXLOOM_ALLOW_MISSING_GIT"

// allowMissingGit is captured at package init, on purpose — the same reason
// dockergate captures its own knob there. Isolate clears every EnvKeys
// variable (this one included, so the enforcement test stays honest), so a
// fixture consulted after a test isolates would otherwise silently demote
// itself back to skipping.
var allowMissingGit = os.Getenv(EnvAllowMissingGit) == "1"

// requireGit fails, rather than skips, when git is not on PATH.
//
// A skip here is invisible: the 16 call sites of RealGitWorktreeFixture are
// the only coverage of the linked-worktree redirect logic, and they are the
// tests most likely to matter in an environment odd enough to lack git. A
// skip means every one of them evaporates and the suite still reports PASS —
// the silent-no-op shape these tests exist to catch, inside the fixture that
// serves them. Unlike a container runtime, git is not optional here: this
// repository is a git checkout, its build stamps a commit sha, and its
// project-root resolution shells out to git, so "no git" is a broken
// environment rather than a legitimate one.
//
// EnvAllowMissingGit=1 restores the skip for anyone who genuinely needs it,
// and the failure message names it.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err == nil {
		return
	}
	if allowMissingGit {
		t.Skipf("git not on PATH and %s=1: this test ran NOTHING", EnvAllowMissingGit)
		return
	}
	t.Fatalf("git is not on PATH, so the linked-worktree fixture cannot build a repo and this "+
		"test would silently cover nothing. git is a prerequisite for this repository, not an "+
		"optional dependency; set %s=1 to downgrade this to a skip.", EnvAllowMissingGit)
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
