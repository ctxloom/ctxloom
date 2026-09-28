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
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The cells adapter is the ONE place a launch's environment is prepared;
// these pin what a Cell carries out of it and what the codec then delivers
// onto the wire.

// The session names the cases key their homes by, and the token a stored
// setup-token stands in for.
const (
	harpA        = "ugly-icy-squid"
	harpB        = "brave-warm-otter"
	tokenFixture = "sk-ant-oat01-fixture"
)

// harpDir is harp's session dir under the (test) ctxloom home — where
// Resolve places it.
func harpDir(t *testing.T, harp string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	return dir
}

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
	t.Setenv(claude.OAuthTokenEnv, tokenFixture) // the token agent's credential, exported
	repo := initIsolationTestRepo(t)
	req := claudeKind(t)
	req.Axes.Workspace = launch.WorkspaceWorktree
	req.ProjectRoot = repo
	req.SessionDir = harpDir(t, "test-harp")

	cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cell.Cleanup() })

	prepared, ok := EnvironmentOf(cell)
	require.True(t, ok)
	require.Equal(t, "worktree", prepared.Describe().Workspace, "a git repo + worktree axis must resolve a worktree, not degrade to none")
	require.NotEqual(t, repo, cell.Workspace, "the resolved workspace must be a distinct worktree checkout, not the shared project dir")

	require.Contains(t, cell.Env, "TMPDIR", "the per-agent toolchain scratch dir")
	require.Contains(t, cell.Env, "GIT_AUTHOR_NAME", "the per-agent git identity")
	assert.NotContains(t, cell.Env, claude.ConfigDirEnv, "the config home is not the workspace's to carry")

	// The cell's env is delivered to the engine beneath the identity carriers.
	env := launch.Launch{Identity: req.Identity, Cell: cell}.EngineEnv()
	assert.Equal(t, "test-harp", env[sessions.EnvHarp], "the session identity survives the merge")
	assert.Equal(t, cell.Env["TMPDIR"], env["TMPDIR"], "the isolation-resolved workspace env reaches the engine env")
}

func TestCellsPrepare_SessionHome(t *testing.T) {
	prepare := func(t *testing.T, workDir string, home launch.HomeMode, workspace launch.WorkspaceAxis, harp string) launch.Cell {
		t.Helper()
		req := claudeKind(t)
		req.ProjectRoot = workDir
		req.HomeMode = home
		req.Auth = string(engine.AuthLogin)
		req.Axes.Workspace = workspace
		req.Identity.Harp = harp
		req.Env = map[string]string{sessions.EnvHarp: harp}
		req.SessionDir = harpDir(t, harp)
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
		assert.Contains(t, cell.Env, claude.SecureStorageEnv, "a host login agent's cell carries the shared login")
		assert.NotContains(t, cell.Env, claude.OAuthTokenEnv)
		assert.Contains(t, cell.Unset, claude.OAuthTokenEnv, "and every other credential is unset")
		assert.Equal(t, want, cell.Paths.Paths().SessionHome.Host, "the engine home is a root the launch advises")
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

		env, ok := EnvironmentOf(cell)
		require.True(t, ok)
		require.Equal(t, "worktree", env.Describe().Workspace, "the worktree axis must not have degraded")
		require.NotEqual(t, repo, cell.Workspace)
		want := claudeInstanceDir(t, repo, "test-harp")
		assert.Equal(t, want, cell.Env[claude.ConfigDirEnv],
			"the worktree cell's home is the session instance under the PROJECT root, exactly as on the live tree")

		cfg, err := os.ReadFile(filepath.Join(want, ".claude.json"))
		require.NoError(t, err)
		assert.Contains(t, string(cfg), cell.Workspace, "the trust answer names the checkout the engine runs in")
	})
}

// A claude CHILD of a mock-engine owner. The owner's session keeps no claude
// home at all, and the child's cell still prepares: its agent's token mode
// resolves to the stored token in the CELL's env, never this process's, with
// every other credential blanked. Nothing is read from the owner's session
// home, so the engine the owner runs is irrelevant.
func TestCellsPrepare_ClaudeChildOfAMockOwnerNeedsNothingFromTheOwner(t *testing.T) {
	resetStrictness(t)
	fakeHostHome(t, "")
	t.Setenv(claude.APIKeyEnv, "sk-ant-api-shell")
	_, err := isolation.StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(tokenFixture))
	require.NoError(t, err)

	ownerHome, err := paths.HarpSessionEngineHomes(harpB)
	require.NoError(t, err)

	workDir := t.TempDir()
	req := claudeKind(t)
	req.ProjectRoot = workDir
	req.HomeMode = launch.HomeModeSession
	req.Auth = string(engine.AuthToken)
	req.Identity = sessions.Identity{Harp: harpA, Depth: 1}
	req.Env = map[string]string{sessions.EnvHarp: harpA}
	req.SessionDir = harpDir(t, harpA)
	cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cell.Cleanup() })

	assert.Empty(t, strictness.All(), "the child's home is not refused")
	assert.Equal(t, claudeInstanceDir(t, workDir, harpA), cell.Env[claude.ConfigDirEnv])
	assert.Equal(t, tokenFixture, cell.Env[claude.OAuthTokenEnv], "the agent's mode resolved to the stored token")
	assert.NotContains(t, cell.Env, claude.APIKeyEnv)
	assert.Contains(t, cell.Unset, claude.APIKeyEnv, "the shell's key is unset: the declared mode decides")
	assert.Empty(t, os.Getenv(claude.OAuthTokenEnv), "the stored token never enters this process's env")
	assert.NoFileExists(t, filepath.Join(cell.Env[claude.ConfigDirEnv], ".credentials.json"))
	assert.NoDirExists(t, ownerHome, "the owner's session home is never consulted")
}

// UNATTENDED with nothing stored: the cell is refused before anything is
// built, naming the command that mints the credential.
func TestCellsPrepare_UnattendedWithNoCredentialIsRefused(t *testing.T) {
	resetStrictness(t)
	fakeHostHome(t, "")
	withTerminal(t, engine.Terminal{}, false)
	// No engine binary is reachable: a regression that tried to mint here
	// fails on a missing binary instead of starting a real login flow.
	t.Setenv("PATH", t.TempDir())

	req := claudeKind(t)
	req.ProjectRoot = t.TempDir()
	req.HomeMode = launch.HomeModeSession
	req.Auth = string(engine.AuthToken)
	req.Identity = sessions.Identity{Harp: harpA, Depth: 1}
	req.Env = map[string]string{sessions.EnvHarp: harpA}
	_, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Contains(t, remedyOf(t, err), "ctxloom auth mint --engine claude-code --mode token")
	root, perr := paths.HarpSessionEngineHomes(harpA)
	require.NoError(t, perr)
	assert.NoDirExists(t, root, "nothing is built for a refused run")
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

// Auth is purely per agent: a binding that selects the human's real home
// (engine_home: host) still authenticates in its declared mode. A token
// agent there gets the stored token and the login's storage var unset; with
// nothing stored and no terminal it is refused like any other token agent.
func TestCellsPrepare_HostHomeAppliesTheDeclaredAuth(t *testing.T) {
	prepare := func(t *testing.T) (launch.Cell, error) {
		req := claudeKind(t)
		req.ProjectRoot = t.TempDir()
		req.HomeMode = launch.HomeModeHost
		req.Identity = sessions.Identity{Harp: harpA}
		req.Env = map[string]string{sessions.EnvHarp: harpA}
		cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
		if err == nil {
			t.Cleanup(func() { _ = cell.Cleanup() })
		}
		return cell, err
	}
	t.Run("stored token", func(t *testing.T) {
		resetStrictness(t)
		fakeHostHome(t, "")
		_, err := isolation.StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(tokenFixture))
		require.NoError(t, err)
		cell, err := prepare(t)
		require.NoError(t, err)
		assert.Equal(t, tokenFixture, cell.Env[claude.OAuthTokenEnv])
		assert.Contains(t, cell.Unset, claude.SecureStorageEnv)
	})
	t.Run("nothing stored, unattended", func(t *testing.T) {
		resetStrictness(t)
		fakeHostHome(t, "")
		withTerminal(t, engine.Terminal{}, false)
		t.Setenv("PATH", t.TempDir())
		_, err := prepare(t)
		require.ErrorIs(t, err, engine.ErrNoCredential)
	})
}

// TestCellsPrepare_AnEngineThatRelocatesNothingGetsTheRulesSessionHome: the
// real cell advises the session home launch.SessionHome names for an engine
// with no relocatable home (mock), and none when the binding selects
// engine_home: host — the same rule the preview cell and the resolver's test
// cell follow.
func TestCellsPrepare_AnEngineThatRelocatesNothingGetsTheRulesSessionHome(t *testing.T) {
	eng, ok := engines.Registry().Lookup("mock")
	require.True(t, ok)
	require.False(t, eng.Home().Relocates(), "the case needs an engine that relocates nothing")
	for _, mode := range []launch.HomeMode{launch.HomeModeSession, launch.HomeModeHost} {
		resetStrictness(t)
		t.Setenv("HOME", t.TempDir())
		req := claudeKind(t)
		req.Engine = eng
		req.HomeMode = mode
		req.ProjectRoot = t.TempDir()
		req.SessionDir = t.TempDir()
		cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cell.Cleanup() })
		want, _ := launch.SessionHome(req.SessionDir, eng, agents.HomeMode(mode))
		require.Equal(t, want, cell.Paths.Paths().SessionHome.Host, "engine_home %s", mode)
	}
}

// NO RUNTIME BRANCH: the cell IS the environment's outcome — its Placement,
// its Listen and the environment itself as the handle — whichever axes were
// asked for. A stand-in environment with container-shaped roots and a
// non-zero listen is handed back unchanged on host and container axes alike;
// a cells adapter that branched on the runtime would rewrite one of them.
func TestCellsPrepare_TheCellIsTheEnvironmentsOutcome(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	want := stubEnvironment{placement: launch.Placement{
		Paths: present.Advised(present.Paths{
			ProjectRoot: present.Root{Host: "/host/proj", Engine: "/ctr/proj"},
			SessionHome: present.Root{Host: "/host/home", Engine: "/home/ctxloom"},
		}),
		Env:  map[string]string{"A_HOME_VAR": "/home/ctxloom"},
		Home: []engine.HomeBinding{{Var: "A_HOME_VAR", Path: "/home/ctxloom"}},
	}}
	prev := prepareEnvironment
	prepareEnvironment = func(context.Context, launch.CellRequest, isolation.Spec) (isolation.Environment, error) {
		return listening{want}, nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })

	for _, runtime := range []launch.RuntimeAxis{launch.RuntimeHost, launch.RuntimeRootless, launch.RuntimeRootful} {
		req := claudeKind(t)
		req.Axes.Runtime = runtime
		req.ProjectRoot = "/host/proj"
		req.SessionDir = harpDir(t, "test-harp")
		cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
		require.NoError(t, err, "runtime %s", runtime)

		assert.Equal(t, want.placement, cell.Placement, "runtime %s: the Placement is the environment's, untouched", runtime)
		assert.Equal(t, present.Listen{Addr: "10.0.0.1"}, cell.Listen, "runtime %s: the Listen is the environment's", runtime)
		env, ok := EnvironmentOf(cell)
		require.True(t, ok)
		assert.Equal(t, listening{want}, env, "runtime %s: the handle is the environment itself", runtime)
		assert.Nil(t, cell.Container, "runtime %s: nothing beside the environment describes the runtime", runtime)
	}
}

// listening is a stubEnvironment with a non-zero Listen.
type listening struct{ stubEnvironment }

func (listening) Listen() present.Listen { return present.Listen{Addr: "10.0.0.1"} }
