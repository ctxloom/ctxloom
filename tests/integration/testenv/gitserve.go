//go:build integration

package testenv

import (
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
// contain slashes (e.g. ".ctxloom/content/bundles/x.yaml"). The repo is created under
// t.TempDir(), so it is cleaned up automatically.
//
// This is the *testing.T-fatal wrapper around SeedRemote (gitserve_acceptance.go)
// — the same init-bare/init-work/config/write/add/commit/push/symbolic-ref
// sequence used to live here a second time, hand-duplicated with its own
// runGit/gitConfig twins (U163-F04). SeedRemote is the one implementation now;
// this trades its returned error for t.Fatalf, matching every other
// *testing.T helper in this package.
func SeedGitRepo(t *testing.T, files map[string]string) *GitRepo {
	t.Helper()

	e := &TestEnvironment{Root: t.TempDir()}
	url, err := e.SeedRemote(files)
	if err != nil {
		t.Fatalf("SeedGitRepo: %v", err)
	}
	bare := strings.TrimPrefix(url, "file://")

	sha, err := gitOutput(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("SeedGitRepo: rev-parse HEAD: %v", err)
	}

	return &GitRepo{
		Dir: bare,
		URL: url,
		SHA: strings.TrimSpace(sha),
	}
}

// CommitFile adds/updates a file on main and pushes it, returning the new tip
// SHA. Used to exercise the fetch/update path after an initial clone.
//
// Wraps AdvanceRemote (gitserve_acceptance.go) the same way SeedGitRepo wraps
// SeedRemote — see that doc.
func (r *GitRepo) CommitFile(t *testing.T, relPath, content string) string {
	t.Helper()

	e := &TestEnvironment{Root: t.TempDir()}
	if err := e.AdvanceRemote(r.Dir, map[string]string{relPath: content}); err != nil {
		t.Fatalf("CommitFile: %v", err)
	}

	sha, err := gitOutput(r.Dir, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("CommitFile: rev-parse HEAD: %v", err)
	}
	r.SHA = strings.TrimSpace(sha)
	return r.SHA
}

// CtxloomV1Layout returns a minimal ctxloom repo layout (a single bundle)
// suitable for seeding a GitRepo. The bundle ships an mcp server and a hook so
// resolution/apply can be asserted. (Top-level profile distribution was retired.)
func CtxloomV1Layout() map[string]string {
	return map[string]string{
		".ctxloom/content/bundles/demo.yaml": strings.TrimSpace(`
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
	}
}
