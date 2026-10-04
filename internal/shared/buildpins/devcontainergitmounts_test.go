package buildpins

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const devcontainerGitMountsScript = "../../../scripts/devcontainer-git-mounts.sh"

// workspaceDst is where _run mounts the checkout inside the dev container.
const workspaceDst = "/workspace"

// gitRepoWithWorktrees is a throwaway repository with two linked worktrees
// registered beside its main checkout — the shape every agent machine has.
type gitRepoWithWorktrees struct {
	main, linked, other string
	common              string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newGitRepoWithWorktrees(t *testing.T) gitRepoWithWorktrees {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := gitRepoWithWorktrees{
		main:   filepath.Join(root, "main"),
		linked: filepath.Join(root, "linked"),
		other:  filepath.Join(root, "other"),
	}
	if err := os.MkdirAll(r.main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, r.main, "init", "-q", "-b", "main")
	runGit(t, r.main, "commit", "-q", "--allow-empty", "-m", "root")
	runGit(t, r.main, "worktree", "add", "-q", r.linked, "-b", "linked")
	runGit(t, r.main, "worktree", "add", "-q", r.other, "-b", "other")
	r.common = filepath.Join(r.main, ".git")
	return r
}

// gitMounts runs the script from dir and returns the --mount specs it prints,
// plus the mask dir it was handed.
func gitMounts(t *testing.T, dir string) ([]string, string) {
	t.Helper()
	mask := t.TempDir()
	cmd := exec.Command("bash", mustAbs(t, devcontainerGitMountsScript), dir, workspaceDst, mask)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("devcontainer-git-mounts.sh: %v\n%s", err, stderr)
	}
	var specs []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if line != "--mount" {
			specs = append(specs, line)
		}
	}
	return specs, mask
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func bindSpec(src, dst string, readonly bool) string {
	s := "type=bind,src=" + src + ",dst=" + dst
	if readonly {
		s += ",readonly"
	}
	return s
}

// From a linked worktree, the container gets the common dir read-write (git
// needs it: refs, packed-refs and config are lock-and-renamed inside it), the
// worktrees/ registry masked by an empty read-only dir, and only this
// checkout's own admin dir mounted back. Without the mask, a `git worktree
// prune` or gc auto-prune in the container deletes every OTHER worktree's
// registration on the host, because none of their checkouts are mounted.
//
// The checkout is ALSO bound at its own absolute path: its registration's
// back-pointer names that path, and _run mounts the checkout at the workspace
// path, so without it prune finds the back-pointer dangling and empties this
// checkout's own admin dir through the read-write mount.
func TestDevcontainerGitMounts_LinkedWorktreeMasksOtherRegistrations(t *testing.T) {
	r := newGitRepoWithWorktrees(t)
	specs, mask := gitMounts(t, r.linked)

	registry := filepath.Join(r.common, "worktrees")
	admin := filepath.Join(registry, "linked")
	want := []string{
		bindSpec(r.common, r.common, false),
		bindSpec(mask, registry, true),
		bindSpec(admin, admin, false),
		bindSpec(r.linked, r.linked, false),
	}
	if !slices.Equal(specs, want) {
		t.Fatalf("mounts\n got %q\nwant %q", specs, want)
	}
	entries, err := os.ReadDir(mask)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "linked" || !entries[0].IsDir() {
		t.Fatalf("mask must hold exactly this checkout's admin-dir mountpoint, got %v", entries)
	}
}

// The main checkout's git dir is inside the workspace mount, so its registry
// reaches the container through that mount, at the workspace path — and a
// main checkout has no admin dir of its own in the registry to mount back.
func TestDevcontainerGitMounts_MainCheckoutMasksTheRegistryInsideTheWorkspace(t *testing.T) {
	r := newGitRepoWithWorktrees(t)
	specs, mask := gitMounts(t, r.main)

	want := []string{bindSpec(mask, workspaceDst+"/.git/worktrees", true)}
	if !slices.Equal(specs, want) {
		t.Fatalf("mounts\n got %q\nwant %q", specs, want)
	}
	entries, err := os.ReadDir(mask)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a main checkout's mask must be empty, got %v", entries)
	}
}

// No registry, nothing to mask and nothing outside the workspace to mount.
func TestDevcontainerGitMounts_MainCheckoutWithoutWorktreesMountsNothing(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	specs, _ := gitMounts(t, dir)
	if len(specs) != 0 {
		t.Fatalf("want no mounts, got %q", specs)
	}
}
