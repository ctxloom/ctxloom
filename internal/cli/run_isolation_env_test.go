package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
