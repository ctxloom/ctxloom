package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The cells adapter is the ONE place a launch's workspace is prepared and
// its engine home bound; these pin what a Cell carries out of it and what
// the codec then delivers onto the wire.

func claudeKind(t *testing.T) launch.CellRequest {
	t.Helper()
	eng, ok := engines.Registry().Lookup("claude-code")
	require.True(t, ok)
	return launch.CellRequest{
		Axes:     launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost},
		Engine:   eng,
		Identity: sessions.Identity{Harp: "test-harp"},
		HomeMode: launch.HomeModeHost,
		Env:      map[string]string{sessions.EnvHarp: "test-harp"},
	}
}

func TestCellsPrepare_WorktreeDeliversWorkspaceEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the worktree isolation-env integration test")
	}
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := initIsolationTestRepo(t)
	req := claudeKind(t)
	req.Axes.Workspace = launch.WorkspaceWorktree
	req.ProjectRoot = repo

	cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cell.Cleanup() })

	handle, ok := TransportOf(cell)
	require.True(t, ok)
	require.Equal(t, "worktree", handle.Policy.Name(), "a git repo + worktree axis must resolve the Worktree policy, not degrade to none")
	require.NotEqual(t, repo, cell.Workspace, "the resolved workspace must be a distinct worktree checkout, not the shared project dir")

	require.Contains(t, cell.Env, "TMPDIR", "the per-agent toolchain scratch dir")
	require.Contains(t, cell.Env, "GIT_AUTHOR_NAME", "the per-agent git identity")
	assert.NotContains(t, cell.Env, claude.ConfigDirEnv, "the config home is not the workspace's to carry")

	// The cell's env is delivered to the engine beneath the identity carriers.
	env := launch.Launch{Identity: req.Identity, Cell: cell}.EngineEnv()
	assert.Equal(t, "test-harp", env[sessions.EnvHarp], "the session identity survives the merge")
	assert.Equal(t, cell.Env["TMPDIR"], env["TMPDIR"], "the isolation-resolved workspace env reaches the engine env")
}

func TestCellsPrepare_InTreeAgentHome(t *testing.T) {
	prepare := func(t *testing.T, workDir string, home launch.HomeMode, workspace launch.WorkspaceAxis, harp string) launch.Cell {
		t.Helper()
		req := claudeKind(t)
		req.ProjectRoot = workDir
		req.HomeMode = home
		req.Axes.Workspace = workspace
		req.Identity.Harp = harp
		req.Env = map[string]string{sessions.EnvHarp: harp}
		cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cell.Cleanup() })
		return cell
	}

	t.Run("a binding that declares engine_home: session gets the controlled home", func(t *testing.T) {
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ANTHROPIC_API_KEY", "sk-test") // authenticates without a host credential fixture
		workDir := t.TempDir()
		cell := prepare(t, workDir, launch.HomeModeSession, launch.WorkspaceNone, "test-harp")

		want := claudeInstanceDir(t, workDir, "test-harp")
		assert.Equal(t, want, cell.Env[claude.ConfigDirEnv])
		assert.Contains(t, want, "test-harp", "the home is THIS session's")
		require.Len(t, cell.Home, 1, "the binding the cell made is reported on it")
		assert.Equal(t, claude.ConfigDirEnv, cell.Home[0].Var)
		assert.Equal(t, want, cell.Paths.Paths().EngineHome.Host, "the engine home is a root the launch advises")
	})

	t.Run("a binding selecting engine_home: host keeps the real host home", func(t *testing.T) {
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		workDir := t.TempDir()
		cell := prepare(t, workDir, launch.HomeModeHost, launch.WorkspaceNone, "test-harp")

		assert.NotContains(t, cell.Env, claude.ConfigDirEnv, "a host engine_home must keep the real ~/.claude")
		assert.Empty(t, cell.Home)
		assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"))
	})

	t.Run("two sessions in one checkout get two instances", func(t *testing.T) {
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		workDir := t.TempDir()
		homes := map[string]string{}
		for _, harp := range []string{"ugly-icy-squid", "brave-warm-otter"} {
			cell := prepare(t, workDir, launch.HomeModeSession, launch.WorkspaceNone, harp)
			got := cell.Env[claude.ConfigDirEnv]
			require.NotEmpty(t, got, "%s: the controlled home must be contributed", harp)
			homes[harp] = got
		}
		assert.NotEqual(t, homes["ugly-icy-squid"], homes["brave-warm-otter"],
			"two concurrent sessions in one checkout must not share one CLAUDE_CONFIG_DIR")
	})

	t.Run("a worktree cell gets the same session home as the live tree", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not on PATH; skipping the worktree cell case")
		}
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		repo := initIsolationTestRepo(t)
		cell := prepare(t, repo, launch.HomeModeSession, launch.WorkspaceWorktree, "test-harp")

		handle, ok := TransportOf(cell)
		require.True(t, ok)
		require.Equal(t, "worktree", handle.Policy.Name(), "the worktree axis must not have degraded")
		require.NotEqual(t, repo, cell.Workspace)
		want := claudeInstanceDir(t, repo, "test-harp")
		assert.Equal(t, want, cell.Env[claude.ConfigDirEnv],
			"the worktree cell's home is the session instance under the PROJECT root, exactly as on the live tree")
		assert.NotContains(t, isolation.WorkspaceEnv(handle.Workspace), claude.ConfigDirEnv,
			"the workspace itself carries no config-home var — one carrier, not two")

		cfg, err := os.ReadFile(filepath.Join(want, ".claude.json"))
		require.NoError(t, err)
		assert.Contains(t, string(cfg), cell.Workspace, "the trust answer names the checkout the engine runs in")
	})
}

// A claude CHILD of a mock-engine owner. The owner's session keeps no claude
// home at all, and the child's cell still prepares, from the stored
// setup-token alone: ExportStoredTokens puts it in this process's env before
// any launch, and that env is what every runner, host or container,
// inherits. Nothing is read from the owner's session home, so the engine the
// owner runs is irrelevant.
func TestCellsPrepare_ClaudeChildOfAMockOwnerAuthenticatesFromTheStoredToken(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	a, ok := isolation.TokenAuthFor("claude-code")
	require.True(t, ok)
	for _, v := range append([]string{a.TokenVar}, a.EnvTriggers...) {
		t.Setenv(v, "")
	}
	require.NoError(t, os.Unsetenv(a.TokenVar))
	_, err := isolation.StoreEngineToken("claude-code", []byte(tokenFixture))
	require.NoError(t, err)
	require.NoError(t, isolation.ExportStoredTokens())

	ownerHome, err := paths.HarpSessionEngineHomes(harpB)
	require.NoError(t, err)

	workDir := t.TempDir()
	req := claudeKind(t)
	req.ProjectRoot = workDir
	req.HomeMode = launch.HomeModeSession
	req.Identity = sessions.Identity{Harp: harpA, Depth: 1}
	req.Env = map[string]string{sessions.EnvHarp: harpA}
	cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cell.Cleanup() })

	assert.Empty(t, strictness.All(), "the child's home is not refused")
	assert.Equal(t, claudeInstanceDir(t, workDir, harpA), cell.Env[claude.ConfigDirEnv])
	assert.Equal(t, tokenFixture, os.Getenv(a.TokenVar), "the stored token is in the env every runner inherits")
	assert.NoFileExists(t, filepath.Join(cell.Env[claude.ConfigDirEnv], ".credentials.json"))
	assert.NoDirExists(t, ownerHome, "the owner's session home is never consulted")
}

func initIsolationTestRepo(t *testing.T) string {
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

func claudeInstanceDir(t *testing.T, workDir, harp string) string {
	t.Helper()
	root, err := paths.HarpSessionEngineHomes(harp)
	require.NoError(t, err)
	return filepath.Join(root, claude.HomeLeaf)
}
