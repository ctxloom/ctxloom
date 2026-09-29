package operations

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
	fakeHostHome(t, "")
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
		fakeHostHome(t, "login") // the login agent's shared store, present on the host
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
		fakeHostHome(t, "login")
		workDir := t.TempDir()
		cell := prepare(t, workDir, launch.HomeModeHost, launch.WorkspaceNone, "test-harp")

		assert.NotContains(t, cell.Env, claude.ConfigDirEnv, "a host engine_home must keep the real ~/.claude")
		assert.Empty(t, cell.Home)
		assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"))
	})

	t.Run("two sessions in one checkout get two instances", func(t *testing.T) {
		resetStrictness(t)
		fakeHostHome(t, "login")
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
		fakeHostHome(t, "login")
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
		req.SessionDir = harpDir(t, harpA)
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
		fakeHostHome(t, "")
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
// asked for, and the credentials handed to the environment are the declared
// mode's own on every runtime. A stand-in environment with container-shaped
// roots and a non-zero listen is handed back unchanged on host and container
// axes alike; a cells adapter that branched on the runtime would rewrite one
// of them, or hand a container different credentials.
func TestCellsPrepare_TheCellIsTheEnvironmentsOutcome(t *testing.T) {
	resetStrictness(t)
	fakeHostHome(t, "")
	t.Setenv(claude.OAuthTokenEnv, tokenFixture)
	want := stubEnvironment{placement: launch.Placement{
		Paths: present.Advised(present.Paths{
			ProjectRoot: present.Root{Host: "/host/proj", Engine: "/ctr/proj"},
			SessionHome: present.Root{Host: "/host/home", Engine: "/home/ctxloom"},
		}),
		Env:  map[string]string{"A_HOME_VAR": "/home/ctxloom"},
		Home: []engine.HomeBinding{{Var: "A_HOME_VAR", Path: "/home/ctxloom"}},
	}}
	var got isolation.Spec
	prev := prepareEnvironment
	prepareEnvironment = func(_ context.Context, _ launch.CellRequest, s isolation.Spec) (isolation.Environment, error) {
		got = s
		return listening{want}, nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
	tokenCreds := engine.Credentials{
		Env:   map[string]string{claude.OAuthTokenEnv: tokenFixture},
		Unset: []string{claude.APIKeyEnv, claude.AuthTokenEnv, "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", claude.SecureStorageEnv},
	}

	for _, runtime := range []launch.RuntimeAxis{launch.RuntimeHost, launch.RuntimeRootless, launch.RuntimeRootful} {
		req := claudeKind(t)
		req.Axes.Runtime = runtime
		req.ProjectRoot = "/host/proj"
		req.SessionDir = harpDir(t, "test-harp")
		cell, err := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}.Prepare(context.Background(), req)
		require.NoError(t, err, "runtime %s", runtime)

		spec, err := isolation.NewSpec(req.Axes, req.Engine).Project(req.ProjectRoot).
			Session("test-harp", req.SessionDir, isolation.SessionStateFromEnv(req.Env)).
			Home(agents.HomeMode(req.HomeMode)).Credentials(tokenCreds).Build()
		require.NoError(t, err)
		// Rendered, not compared: the engine value carries funcs, which
		// reflect.DeepEqual never equates; the rendering names the same engine
		// instance and every other field, credentials included.
		assert.Equal(t, fmt.Sprintf("%+v", spec), fmt.Sprintf("%+v", got), "runtime %s: the environment is handed the declared mode's credentials, whatever the runtime", runtime)
		assert.Equal(t, want.placement, cell.Placement, "runtime %s: the Placement is the environment's, untouched", runtime)
		assert.Equal(t, present.Listen{Addr: "10.0.0.1"}, cell.Listen, "runtime %s: the Listen is the environment's", runtime)
		env, ok := EnvironmentOf(cell)
		require.True(t, ok)
		assert.Equal(t, listening{want}, env, "runtime %s: the handle is the environment itself", runtime)
	}
}

// listening is a stubEnvironment with a non-zero Listen.
type listening struct{ stubEnvironment }

func (listening) Listen() present.Listen { return present.Listen{Addr: "10.0.0.1"} }

// A PREVIEW cell refuses nothing: the isolation findings a run's cell gate
// refuses on stay on the ledger for the dry run's own gate, which lists them
// after the plan, and the preview carries on with the environment's
// best-effort outcome. The real cell, over the same findings, refuses.
func TestCellsPrepare_APreviewRecordsWhatARunRefuses(t *testing.T) {
	resetStrictness(t)
	// The run authenticates first: a stored token, so the refusal it meets
	// is the environment's, not the credential's.
	fakeHostHome(t, "")
	_, err := isolation.StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(tokenFixture))
	require.NoError(t, err)
	refusing := func(context.Context, launch.CellRequest, isolation.Spec) (isolation.Environment, error) {
		strictness.FailAlways(report.KindIsolation, "the fix", "the requested boundary cannot be provided")
		return stubEnvironment{}, nil
	}
	prevPrepare, prevPreview := prepareEnvironment, previewEnvironment
	prepareEnvironment, previewEnvironment = refusing, refusing
	t.Cleanup(func() { prepareEnvironment, previewEnvironment = prevPrepare, prevPreview })
	req := claudeKind(t)
	req.ProjectRoot = t.TempDir()
	req.Auth = string(engine.AuthToken)
	req.SessionDir = harpDir(t, "test-harp")
	cells := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	preview := cells
	preview.preview = true
	_, err = preview.Prepare(context.Background(), req)
	require.NoError(t, err, "a preview carries on past a refusal")
	require.Len(t, strictness.Since(mark), 1, "the finding stays on the ledger for the dry run's gate")

	_, err = cells.Prepare(context.Background(), req)
	require.ErrorIs(t, err, launch.ErrRuntimeUnavailable, "a run refuses on the same finding")
}

// A PREVIEW sees the credential stores a run would share: a login whose
// store is missing is the run's refusal, and the dry run records it —
// same message, same remedy — beside the plan instead of showing a clean
// setup the run then refuses.
func TestCellsPrepare_APreviewRecordsTheMissingStoreARunRefuses(t *testing.T) {
	resetStrictness(t)
	fakeHostHome(t, "") // no ~/.claude: the login store a run shares is missing
	req := claudeKind(t)
	req.ProjectRoot = t.TempDir()
	req.Auth = string(engine.AuthLogin)
	req.SessionDir = harpDir(t, "test-harp")
	cells := Cells{engines: engines.Registry(), cfg: config.NewFixture(config.Fixture{})}

	_, runErr := cells.Prepare(context.Background(), req)
	require.ErrorIs(t, runErr, engine.ErrNoCredential, "precondition: a run refuses on the missing store")

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	preview := cells
	preview.preview = true
	_, err := preview.Prepare(context.Background(), req)
	require.NoError(t, err, "a preview carries on past a refusal")
	found := strictness.Since(mark)
	require.Len(t, found, 1, "the run's refusal is on the dry run's ledger")
	assert.Contains(t, found[0].Text, runErr.Error())
	assert.Equal(t, remedyOf(t, runErr), found[0].Remedy)
	assert.True(t, found[0].NonDegradable)
}

// A preview resolves credentials READ-ONLY: it never mints, never stores,
// and never carries a secret into the placement it shows. Where a run
// would refuse (unattended, nothing stored) the preview records that
// refusal; where a run would mint at the human's terminal it refuses
// nothing, and mints nothing either.
func TestCellsPrepare_APreviewResolvesCredentialsReadOnly(t *testing.T) {
	prepareFake := func(t *testing.T) (Cells, launch.CellRequest, *[]engine.Terminal) {
		t.Helper()
		resetStrictness(t)
		reg, seen := installFakeMint(t)
		eng, ok := reg.Lookup("fake-auth")
		require.True(t, ok)
		req := launch.CellRequest{
			Axes:        launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost},
			Engine:      eng,
			Identity:    sessions.Identity{Harp: harpA},
			HomeMode:    launch.HomeModeHost,
			ProjectRoot: t.TempDir(),
			SessionDir:  harpDir(t, harpA),
			Auth:        string(engine.AuthToken),
		}
		return Cells{engines: reg, cfg: config.NewFixture(config.Fixture{}), preview: true}, req, seen
	}
	requireNothingStored := func(t *testing.T) {
		t.Helper()
		_, err := isolation.StoredCredentials("fake-auth").Read(engine.AuthToken)
		require.ErrorIs(t, err, engine.ErrNoCredential, "a preview writes no credential")
	}

	t.Run("attended: the run would mint, so nothing is refused and nothing minted", func(t *testing.T) {
		cells, req, seen := prepareFake(t)
		withTerminal(t, engine.Terminal{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, true)
		mark := strictness.Checkpoint()
		defer strictness.Close(mark)

		_, err := cells.Prepare(context.Background(), req)
		require.NoError(t, err)
		assert.Empty(t, *seen, "a preview never starts the mint flow")
		assert.Empty(t, strictness.Since(mark), "a run at a terminal mints rather than refuses")
		requireNothingStored(t)
	})

	t.Run("unattended: the run's refusal is recorded, nothing minted", func(t *testing.T) {
		cells, req, seen := prepareFake(t)
		withTerminal(t, engine.Terminal{}, false)
		mark := strictness.Checkpoint()
		defer strictness.Close(mark)

		_, err := cells.Prepare(context.Background(), req)
		require.NoError(t, err, "a preview carries on past a refusal")
		found := strictness.Since(mark)
		require.Len(t, found, 1)
		assert.Contains(t, found[0].Remedy, "ctxloom auth mint --engine fake-auth --mode token")
		assert.Empty(t, *seen)
		requireNothingStored(t)

		run := cells
		run.preview = false
		_, err = run.Prepare(context.Background(), req)
		require.ErrorIs(t, err, engine.ErrNoCredential, "the run refuses on the same inputs")
		assert.Equal(t, found[0].Remedy, remedyOf(t, err), "with the same remedy")
	})

	t.Run("stored: the preview places the credential's var, never its secret", func(t *testing.T) {
		cells, req, _ := prepareFake(t)
		_, err := isolation.StoreEngineCredential("fake-auth", engine.AuthToken, []byte("stored-secret"))
		require.NoError(t, err)

		cell, err := cells.Prepare(context.Background(), req)
		require.NoError(t, err)
		require.Contains(t, cell.Env, fakeTokenVar, "the preview shows the var the run sets")
		assert.NotContains(t, cell.Env[fakeTokenVar], "stored-secret", "and never the secret")
	})
}
