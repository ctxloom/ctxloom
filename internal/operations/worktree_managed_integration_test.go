package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestWorktreeMember_ManagedConfigLandsInWorktree is the P3 write-enable
// integration gate (real git repo + the worktree policy + a real backend Setup):
// an ISOLATED member's per-member managed config is materialized into the WORKTREE
// cwd, the shared project dir working tree is left untouched, and the worktree's
// common-dir info/exclude carries the artifact patterns (§3.1). It exercises the
// same two load-bearing pieces the fan-out composes — the worktree Workspace and
// the plugin Setup writing the host-assembled ManagedConfig into ws.Dir() — without
// spawning a subprocess. Skips cleanly when git is unavailable.
func TestWorktreeMember_ManagedConfigLandsInWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the worktree managed-config integration test")
	}
	ctx := context.Background()
	repo := initManagedTestRepo(t)

	// The worktree policy provisions the isolated cwd + writes info/exclude.
	ws, err := isolation.NewWorktree(git.NewExec(), "").PrepareWorkspace(ctx, repo, "member-managed")
	require.NoError(t, err, "PrepareWorkspace must add a worktree in a real repo")
	wtDir := ws.Dir()
	t.Cleanup(func() {
		_ = ws.Cleanup()
		// ws.Cleanup() is deliberately WIP-safe: it refuses to remove a dirty
		// worktree so a real developer's uncommitted work is never destroyed
		// (internal/lm/isolation/worktree.go's teardown). This test's own Setup
		// call below writes .mcp.json/.claude/ into the worktree, which makes
		// `git status --porcelain` non-empty and trips that guard on EVERY run
		// (not just a panicking one) — so the checkout under the OS temp dir
		// survives ws.Cleanup() unconditionally. This scratch checkout is
		// disposable (the repo above is a throwaway t.TempDir()), so force it
		// away regardless of "dirty" status: this was measured leaking
		// hundreds of ctxloom-wt-member-managed-* dirs during a single
		// mutation run.
		_ = os.RemoveAll(wtDir)
	})
	// ws.Cleanup() also removes the per-agent config-home (ctxloom-cfg-*)
	// unconditionally, but that removal runs through the same production code
	// gremlins mutates: a mutant that breaks it is "killed" elsewhere while
	// genuinely leaving this dir on disk too. Derive its path via the exported
	// env accessor (this package can't reach isolation's unexported fields)
	// and force it away the same way as wtDir above.
	if cd, ok := isolation.WorkspaceEnv(ws)["CLAUDE_CONFIG_DIR"]; ok && cd != "" {
		configHome := filepath.Dir(cd)
		t.Cleanup(func() { _ = os.RemoveAll(configHome) })
	}

	// The host-assembled managed payload (an MCP server here) the plugin's Setup
	// consumes — mirroring backends.AssembleManagedConfig's result, but hand-built
	// so the assertion is deterministic (config.Load would read the ambient tree).
	managed := &agent.ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{
			"demo": {Command: "echo", Args: []string{"hi"}},
		},
		ManageStatusline: true,
	}

	// Drive the SAME Setup the plugin runs, targeting the worktree cwd. An ISOLATED
	// member runs in a DirectoryIsolated cell (as the host maps the worktree policy
	// via operations.CellKindForPolicy), so a real claude backend writes its
	// well-known files — .mcp.json / .claude/ / CLAUDE.md — into the private WorkDir.
	backend := claude.NewClaudeCode()
	require.NoError(t, backend.Setup(ctx, &agent.SetupRequest{
		WorkDir:   ws.Dir(),
		Fragments: []*agent.Fragment{{Content: "PER-MEMBER CONTEXT BODY"}},
		Managed:   managed,
		CellKind:  agent.CellKindDirectoryIsolated,
	}))

	// Managed config landed in the WORKTREE, not the project dir.
	assert.FileExists(t, filepath.Join(ws.Dir(), ".mcp.json"), "managed .mcp.json is written into the worktree cwd")
	assert.DirExists(t, filepath.Join(ws.Dir(), ".claude"), "managed .claude/ is written into the worktree cwd")

	// The shared project dir working tree is untouched (no per-member config leaks).
	assert.NoFileExists(t, filepath.Join(repo, ".mcp.json"), "the project dir keeps no per-member .mcp.json")
	assert.NoDirExists(t, filepath.Join(repo, ".claude"), "the project dir keeps no per-member .claude/")

	// §3.1: the broadened config excludes are written to the common-dir info/exclude
	// (NOT the tracked .gitignore) so the now-written config never rides a merge-back.
	excl, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	require.NoError(t, err, "the common-dir info/exclude must exist")
	for _, pat := range []string{".mcp.json", ".claude/", ".kiro/", ".ctxloom/cache/"} {
		assert.Contains(t, string(excl), pat, "info/exclude covers %q", pat)
	}
	assert.NoFileExists(t, filepath.Join(repo, ".gitignore"), "excludes must NOT touch the tracked .gitignore")
}

// initManagedTestRepo creates a temp git repo with one commit and a stable
// identity, returning its path.
func initManagedTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
			"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed"), 0o644))
	run("add", "README.md")
	run("commit", "-m", "seed")
	return dir
}
