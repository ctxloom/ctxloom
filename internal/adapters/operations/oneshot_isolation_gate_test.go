package operations

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetStrictness restores pristine strict-mode state for a test and registers
// cleanup, so the package-global finding collector never bleeds between tests
// (mirrors isolation_test.go's helper of the same name).
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
}

// stubWorkspace is a minimal isolation.Workspace for the prepareIsolation stub.
type stubWorkspace struct{ dir string }

func (w stubWorkspace) Dir() string    { return w.dir }
func (w stubWorkspace) Cleanup() error { return nil }

// stubPolicy is a minimal isolation.Policy whose StartRunner hands back an
// inert handle — so a member that PASSES the gate still never starts a real
// runner subprocess. seen, when set, records what StartRunner was asked.
// Name reports "none" so runResolvedAgent resolves the SHARED cell, and with
// it the form that presents the session's surfaces rather than writing its
// own.
type stubPolicy struct{ seen *stubSpawn }

// stubSpawn records the runner starts a stubPolicy saw.
type stubSpawn struct {
	mu       sync.Mutex
	calls    int
	backend  string
	spawnEnv map[string]string
}

func (stubPolicy) Name() string { return isolation.None{}.Name() }
func (p stubPolicy) ResolveWorkspace(_ context.Context, projectDir, _ string) (isolation.Workspace, error) {
	return stubWorkspace{dir: projectDir}, nil
}
func (stubPolicy) Mount(context.Context, isolation.Workspace) (isolation.MountPlan, error) {
	return isolation.MountPlan{}, nil
}
func (p stubPolicy) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (isolation.Workspace, error) {
	ws, err := p.ResolveWorkspace(ctx, projectDir, agentID)
	if err != nil {
		return nil, err
	}
	if _, err := p.Mount(ctx, ws); err != nil {
		return nil, err
	}
	return ws, nil
}
func (p stubPolicy) StartRunner(_ context.Context, backend, _ string, _ int, _ isolation.Workspace, spawnEnv map[string]string) (*isolation.RunnerHandle, error) {
	if p.seen != nil {
		p.seen.mu.Lock()
		p.seen.calls++
		p.seen.backend = backend
		p.seen.spawnEnv = spawnEnv
		p.seen.mu.Unlock()
	}
	return &isolation.RunnerHandle{Kill: func() {}, Wait: func() error { return nil }}, nil
}
func (stubPolicy) InteractiveRunner(context.Context, string, isolation.Workspace, map[string]string) (*exec.Cmd, string, error) {
	return nil, "", nil
}

// stubPrepareIsolation swaps runResolvedAgent's isolation.Prepare seam for one
// that records a fatal ClassIsolation finding for the agentIDs in failFor —
// simulating exactly what prepareChain/chainFor do when an explicitly-requested
// container can't be satisfied — and always returns a host workspace (the
// degrade chain never blocks). Restores the real Prepare on cleanup.
func stubPrepareIsolation(t *testing.T, failFor map[string]bool, seen ...*stubSpawn) {
	var rec *stubSpawn
	if len(seen) > 0 {
		rec = seen[0]
	}
	t.Helper()
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, _ isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, agentID string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		if failFor[agentID] {
			strictness.Fail(strictness.ClassIsolation,
				"install/build the agent image and start the container runtime (docker/podman), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox",
				"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): agent image absent", agentID)
		}
		return stubPolicy{seen: rec}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })
}

// TestIsolationGateErr pins the gate's decision table directly: only strict-mode
// ClassIsolation findings fail a member; degraded mode and foreign classes pass.
func TestIsolationGateErr(t *testing.T) {
	isoFinding := strictness.Finding{
		Class:   strictness.ClassIsolation,
		Message: "container isolation was requested but could not start",
		FixIt:   "start the container runtime, or pass --degraded",
	}

	t.Run("strict + isolation finding → member-fatal error with finding and fix", func(t *testing.T) {
		resetStrictness(t)
		err := isolationGateErr(strictness.Mode{}, []strictness.Finding{isoFinding})
		require.Error(t, err)
		assert.Contains(t, err.Error(), isoFinding.Message)
		assert.Contains(t, err.Error(), isoFinding.FixIt)
	})

	t.Run("strict + several isolation findings → every finding's fix is shown", func(t *testing.T) {
		resetStrictness(t)
		other := strictness.Finding{
			Class:   strictness.ClassIsolation,
			Message: "worktree isolation was requested but the workspace could not be created",
			FixIt:   "commit or stash local changes, or pass --degraded",
		}
		err := isolationGateErr(strictness.Mode{}, []strictness.Finding{isoFinding, other})
		require.Error(t, err)
		assert.Contains(t, err.Error(), isoFinding.FixIt)
		assert.Contains(t, err.Error(), other.FixIt)
	})

	t.Run("strict + no findings → nil", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(strictness.Mode{}, nil))
	})

	t.Run("strict + non-isolation findings only → nil (not this gate's class)", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(strictness.Mode{}, []strictness.Finding{
			{Class: strictness.ClassSync, Message: "sync failed"},
		}))
	})

	t.Run("degraded → nil even with findings", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(strictness.Mode{Degraded: true}, []strictness.Finding{isoFinding}))
	})
}
