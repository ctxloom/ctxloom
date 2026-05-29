//go:build integration

package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitRepo is a local bare git repository seeded with content and served over a
// file:// URL. go-git (the GitCloneFetcher / RepoCache transport) clones and
// fetches a file:// remote identically to https, so this exercises the real
// clone/fetch/cache code path without a container or the network — chosen over a
// Gitea container for reliability (see docs/adr/0015-local-git-test-remote.md).
type GitRepo struct {
	// Dir is the bare repository directory.
	Dir string
	// URL is the file:// clone URL for Dir.
	URL string
	// SHA is the commit SHA at the tip of the default branch (main).
	SHA string
}

// SeedGitRepo creates a bare git repo whose default branch (main) contains the
// given files (path -> content), and returns it. Paths are repo-relative and may
// contain slashes (e.g. "ctxloom/v1/bundles/x.yaml"). The repo is created under
// t.TempDir(), so it is cleaned up automatically.
func SeedGitRepo(t *testing.T, files map[string]string) *GitRepo {
	t.Helper()

	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	runGit(t, root, "init", "--bare", "-b", "main", bare)
	runGit(t, root, "init", "-b", "main", work)
	gitConfig(t, work)

	for relPath, content := range files {
		full := filepath.Join(work, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", relPath, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-m", "seed")
	runGit(t, work, "remote", "add", "origin", bare)
	runGit(t, work, "push", "origin", "main")
	// Point the bare repo's HEAD at main so default-branch clones resolve it.
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	sha := strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD"))

	return &GitRepo{
		Dir: bare,
		URL: "file://" + bare,
		SHA: sha,
	}
}

// CommitFile adds/updates a file on main and pushes it, returning the new tip
// SHA. Used to exercise the fetch/update path after an initial clone.
func (r *GitRepo) CommitFile(t *testing.T, relPath, content string) string {
	t.Helper()
	work := t.TempDir()
	runGit(t, filepath.Dir(work), "clone", r.URL, work)
	gitConfig(t, work)

	full := filepath.Join(work, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-m", "update "+relPath)
	runGit(t, work, "push", "origin", "main")
	r.SHA = strings.TrimSpace(runGit(t, work, "rev-parse", "HEAD"))
	return r.SHA
}

func gitConfig(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Deterministic, hermetic git invocation.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// CtxloomV1Layout returns a minimal ctxloom/v1 repo layout (a bundle plus
// direct + inheriting profiles) suitable for seeding a GitRepo. The bundle
// ships an mcp server and a hook so resolution/apply can be asserted.
func CtxloomV1Layout() map[string]string {
	return map[string]string{
		"ctxloom/v1/bundles/demo.yaml": strings.TrimSpace(`
version: 1.0.0
author: test
description: Demo bundle with an MCP server and a hook
mcp:
  demo-server:
    command: demo-mcp
    args: ["--stdio"]
fragments:
  demo-frag:
    tags: [demo]
    content: |
      Demo fragment content.
hooks:
  session_start:
    - command: echo demo-hook
      type: command
`) + "\n",
		"ctxloom/v1/profiles/base.yaml": strings.TrimSpace(`
description: Base profile referencing the demo bundle directly
bundles:
  - demo
`) + "\n",
		"ctxloom/v1/profiles/child.yaml": strings.TrimSpace(`
description: Child profile that only inherits demo via its parent
parents:
  - base
`) + "\n",
	}
}
