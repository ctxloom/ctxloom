package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
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

	cell, err := Cells{cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
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
		cell, err := Cells{cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
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

// The credential replicator a controlled home starts belongs to the RUN. A
// host token refresh reaches the instance while the cell lives — the seeded
// copy would otherwise be revoked on its next request — and Cleanup ends it,
// so a finished run leaves no watcher writing into a home nothing reads.
func TestCellsPrepare_SessionHomeCredentialFollowsTheHostUntilCleanup(t *testing.T) {
	resetStrictness(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	hostFile := filepath.Join(home, ".claude", claude.CredentialsFileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(hostFile), 0o700))
	require.NoError(t, os.WriteFile(hostFile, []byte(`{"token":"one"}`), 0o600))
	workDir := t.TempDir()

	req := claudeKind(t)
	req.ProjectRoot = workDir
	req.HomeMode = launch.HomeModeSession
	cell, err := Cells{cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cell.Cleanup() })

	instFile := filepath.Join(claudeInstanceDir(t, workDir, "test-harp"), claude.CredentialsFileName)
	reads := func(want string) func() bool {
		return func() bool {
			got, err := os.ReadFile(instFile)
			return err == nil && string(got) == want
		}
	}
	require.Eventually(t, reads(`{"token":"one"}`), 5*time.Second, 25*time.Millisecond)

	require.NoError(t, os.WriteFile(hostFile, []byte(`{"token":"two"}`), 0o600))
	require.Eventually(t, reads(`{"token":"two"}`), 5*time.Second, 25*time.Millisecond,
		"a host refresh must reach the live run's instance")

	require.NoError(t, cell.Cleanup())
	require.NoError(t, os.WriteFile(hostFile, []byte(`{"token":"three"}`), 0o600))
	assert.Never(t, reads(`{"token":"three"}`), time.Second, 50*time.Millisecond,
		"after Cleanup nothing may go on writing into the instance: the replicator is the run's, not the process's")
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
	root, err := paths.HarpSessionHome(harp)
	require.NoError(t, err)
	return filepath.Join(root, claude.HomeLeaf)
}
