package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestMergeWorkspaceEnv pins mergeWorkspaceEnv's precedence contract: the
// isolation-resolved env fills gaps but never clobbers an already-assembled
// env value (session identity / user --env, set before isolation resolves),
// and a nil/empty workspaceEnv (none/container policies) is a true no-op that
// returns the existing map unchanged.
func TestMergeWorkspaceEnv(t *testing.T) {
	t.Run("nil workspaceEnv is a no-op and allocates nothing new", func(t *testing.T) {
		existing := map[string]string{"CTXLOOM_SESSION_HARP": "swift-amber-falcon"}
		got := mergeWorkspaceEnv(existing, nil)
		assert.Equal(t, existing, got)
		// got must be the SAME map (no copy) — mutating it must be visible
		// through existing too, proving the common-case no-op path allocates
		// nothing new for every none/container top-level run.
		got["probe"] = "x"
		assert.Equal(t, "x", existing["probe"])
	})

	t.Run("empty workspaceEnv is a no-op", func(t *testing.T) {
		existing := map[string]string{"CTXLOOM_SESSION_HARP": "swift-amber-falcon"}
		got := mergeWorkspaceEnv(existing, map[string]string{})
		assert.Equal(t, existing, got)
	})

	t.Run("workspaceEnv fills gaps in a nil existing map", func(t *testing.T) {
		got := mergeWorkspaceEnv(nil, map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/cfg/claude"})
		assert.Equal(t, map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/cfg/claude"}, got)
	})

	t.Run("workspaceEnv fills gaps alongside existing vars", func(t *testing.T) {
		existing := map[string]string{"CTXLOOM_SESSION_HARP": "swift-amber-falcon"}
		workspaceEnv := map[string]string{"CLAUDE_CONFIG_DIR": "/tmp/cfg/claude", "KIRO_HOME": "/tmp/cfg/kiro"}
		got := mergeWorkspaceEnv(existing, workspaceEnv)
		assert.Equal(t, map[string]string{
			"CTXLOOM_SESSION_HARP": "swift-amber-falcon",
			"CLAUDE_CONFIG_DIR":    "/tmp/cfg/claude",
			"KIRO_HOME":            "/tmp/cfg/kiro",
		}, got)
	})

	t.Run("an already-assembled var wins over the resolved isolation var", func(t *testing.T) {
		// A caller/session var set before isolation resolves (e.g. an operator's
		// explicit --env CLAUDE_CONFIG_DIR=... override) must survive the merge —
		// isolation fills gaps, it never clobbers.
		existing := map[string]string{"CLAUDE_CONFIG_DIR": "/explicit/override"}
		workspaceEnv := map[string]string{"CLAUDE_CONFIG_DIR": "/resolved/by/isolation"}
		got := mergeWorkspaceEnv(existing, workspaceEnv)
		assert.Equal(t, "/explicit/override", got["CLAUDE_CONFIG_DIR"])
	})
}

// TestTopLevelRunIsolationEnv_WorktreeDeliversConfigHomeEnv is the wiring
// regression test for aged-clasp: `ctxloom run --workspace worktree` at the
// TOP LEVEL never merged isolation.WorkspaceEnv into the wire
// RunOptions.Env, so a worktree-isolated claude or kiro run silently kept
// reading the GLOBAL ~/.claude.json / kiro config instead of the per-agent
// config-home isolation.Prepare had already resolved and seeded — a silent
// isolation no-op. This drives the EXACT two calls run.go's built-in-backend
// branch makes — isolation.Prepare(axes, backend, ...) then
// isolation.WorkspaceEnv(ws) — against a real git worktree, then runs the
// result through mergeWorkspaceEnv exactly as run.go does, and asserts the
// per-engine config-home var actually lands in the final env for both
// claude-code and kiro. (operations/oneshot.go's fan-out path already had
// equivalent coverage — worktree_managed_integration_test.go,
// oneshot_isolation_gate_test.go; this is the top-level path's missing twin.)
func TestTopLevelRunIsolationEnv_WorktreeDeliversConfigHomeEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the worktree isolation-env integration test")
	}

	cases := []struct {
		backend string
		envVar  string
	}{
		{backend: "claude-code", envVar: "CLAUDE_CONFIG_DIR"},
		{backend: "kiro", envVar: "KIRO_HOME"},
	}

	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			// Isolate this run's strictness state: a missing host credential
			// (claude-code has no ANTHROPIC_API_KEY nor a host ~/.claude on a
			// bare test runner) is a fail-loudly finding in strict mode — best-
			// effort for the config-home dir itself (still provisioned, so
			// Env() still reports it), but it must not leak into or fail on
			// other tests' global strictness state. --degraded also silences
			// the credential-seed warning noise for this env-presence check.
			prevDegraded := strictness.Degraded()
			strictness.SetDegraded(true)
			t.Cleanup(func() {
				strictness.Reset()
				strictness.SetDegraded(prevDegraded)
			})

			repo := initIsolationTestRepo(t)
			axes := isolation.Axes{Workspace: isolation.WorkspaceWorktree, Runtime: isolation.RuntimeHost}

			// The exact call run.go's built-in-backend branch makes.
			policy, ws := isolation.Prepare(context.Background(), axes, tc.backend, isolation.ImageConfig{}, repo, "agent-"+tc.backend, isolation.SessionState{})
			t.Cleanup(func() { _ = ws.Cleanup() })

			require.Equal(t, "worktree", policy.Name(), "a git repo + worktree axis must resolve the Worktree policy, not degrade to none")
			require.NotEqual(t, repo, ws.Dir(), "the resolved workspace must be a distinct worktree checkout, not the shared project dir")

			workspaceEnv := isolation.WorkspaceEnv(ws)
			require.NotEmpty(t, workspaceEnv, "%s's worktree workspace must expose a per-engine config-home env", tc.backend)
			require.Contains(t, workspaceEnv, tc.envVar)
			assert.NotEmpty(t, workspaceEnv[tc.envVar])

			// The wiring under test: merge into an already-assembled env exactly
			// as run.go's top-level path does (session identity survives).
			existing := map[string]string{"CTXLOOM_SESSION_HARP": "test-harp"}
			finalEnv := mergeWorkspaceEnv(existing, workspaceEnv)

			assert.Equal(t, "test-harp", finalEnv["CTXLOOM_SESSION_HARP"], "the pre-assembled session env must survive the merge")
			assert.Equal(t, workspaceEnv[tc.envVar], finalEnv[tc.envVar],
				"the isolation-resolved %s must be delivered into the wire RunOptions.Env — this is the aged-clasp gap", tc.envVar)
		})
	}
}

// TestPrepareWorkspace_InTreeAgentHome drives the REAL prepareWorkspace — the
// only place the top-level run decides its engine config home — across the
// three cases the scoping rule turns on. It is a wiring test on purpose: the
// helper's own behaviour is pinned in internal/operations, and what can rot
// here is the CONDITION prepareWorkspace passes it.
func TestPrepareWorkspace_InTreeAgentHome(t *testing.T) {
	newState := func(t *testing.T, workDir string, agentConfigHome string, axes isolation.Axes) *runState {
		t.Helper()
		return &runState{
			ctx:             context.Background(),
			backendName:     "claude-code",
			workDir:         workDir,
			activeHarp:      "test-harp",
			agentConfigHome: agentConfigHome,
			runAxes:         axes,
			req:             &pb.RunStart{Options: &pb.RunOptions{Env: map[string]string{"CTXLOOM_SESSION_HARP": "test-harp"}}},
		}
	}
	hostAxes := isolation.Axes{Workspace: isolation.WorkspaceShared, Runtime: isolation.RuntimeHost}

	t.Run("an agent binding that declares config_home: project gets the controlled home", func(t *testing.T) {
		resetStrictness(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test") // authenticates without a host credential fixture

		workDir := t.TempDir()
		st := newState(t, workDir, agents.ConfigHomeProject, hostAxes)
		st.prepareWorkspace()
		t.Cleanup(st.cleanupWorkspace)

		assert.Equal(t, claude.InTreeConfigDir(workDir), st.req.Options.Env[claude.ConfigDirEnv])
		assert.Equal(t, "test-harp", st.req.Options.Env["CTXLOOM_SESSION_HARP"], "the pre-assembled session env must survive")
	})

	// MUTATION TARGET m1: invert the "undeclared → host" default so an
	// agent-bound run with NO declared config_home resolves to project — this
	// case (agentConfigHome == "project" produced only via ResolveConfigHome's
	// own default, exercised in the operations-layer test) is pinned there;
	// here the headline red is the UNDECLARED-binding case just below, which
	// this same st.prepareWorkspace call must resolve to the real home.
	t.Run("an agent binding with an UNDECLARED config_home keeps the real host home", func(t *testing.T) {
		resetStrictness(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")

		workDir := t.TempDir()
		// agentConfigHome carries the RESOLVED value a ResolvedAgent would hand
		// prepareWorkspace — an undeclared binding resolves to
		// agents.ConfigHomeHost (operations.ResolveConfigHome's default), never
		// the empty string a no-binding run leaves behind.
		st := newState(t, workDir, agents.ConfigHomeHost, hostAxes)
		st.prepareWorkspace()
		t.Cleanup(st.cleanupWorkspace)

		assert.NotContains(t, st.req.Options.Env, claude.ConfigDirEnv,
			"an agent-bound run with an undeclared config_home must keep the real ~/.claude")
		assert.NoDirExists(t, claude.StateHome(workDir))
	})

	// MUTATION TARGET m2: a bug that ignored a declared "host" value (treating
	// every resolved agent binding as project) would make this red.
	t.Run("an agent binding that declares config_home: host keeps the real host home", func(t *testing.T) {
		resetStrictness(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")

		workDir := t.TempDir()
		st := newState(t, workDir, agents.ConfigHomeHost, hostAxes)
		st.prepareWorkspace()
		t.Cleanup(st.cleanupWorkspace)

		assert.NotContains(t, st.req.Options.Env, claude.ConfigDirEnv,
			"a binding that DECLARES config_home: host must keep the real ~/.claude")
		assert.NoDirExists(t, claude.StateHome(workDir))
	})

	t.Run("a run with NO agent binding at all keeps the real host home", func(t *testing.T) {
		resetStrictness(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")

		workDir := t.TempDir()
		st := newState(t, workDir, "", hostAxes)
		st.prepareWorkspace()
		t.Cleanup(st.cleanupWorkspace)

		assert.NotContains(t, st.req.Options.Env, claude.ConfigDirEnv,
			"a run with no agent binding at all must keep the human's own ~/.claude")
		assert.NoDirExists(t, claude.StateHome(workDir))
	})

	t.Run("the worktree axis' own config home is never overridden", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not on PATH; skipping the worktree precedence case")
		}
		resetStrictness(t)
		strictness.SetDegraded(true) // a bare runner has no host claude credential to seed
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")

		repo := initIsolationTestRepo(t)
		st := newState(t, repo, agents.ConfigHomeProject, isolation.Axes{Workspace: isolation.WorkspaceWorktree, Runtime: isolation.RuntimeHost})
		st.prepareWorkspace()
		t.Cleanup(st.cleanupWorkspace)

		require.Equal(t, "worktree", st.policy.Name(), "the worktree axis must not have degraded — the precedence claim needs a real isolation value")
		got := st.req.Options.Env[claude.ConfigDirEnv]
		require.NotEmpty(t, got)
		assert.Equal(t, isolation.WorkspaceEnv(st.ws)[claude.ConfigDirEnv], got,
			"isolation's per-agent config home must win — the in-tree contribution fills gaps only")
		assert.NotEqual(t, claude.InTreeConfigDir(repo), got)
		assert.NoDirExists(t, claude.StateHome(repo), "the losing in-tree arm must not even create its home")
	})
}

// initIsolationTestRepo creates a temp git repo with one commit and a stable
// identity, returning its path. Mirrors operations.initManagedTestRepo
// (unexported there, so duplicated here rather than exported cross-package
// for a single test file).
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
