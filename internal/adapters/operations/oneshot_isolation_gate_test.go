package operations

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
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

// stubEnvironment is a minimal isolation.Environment: a host placement over
// one directory whose Start hands back an inert handle — so a member that
// PASSES the gate still never starts a real runner subprocess. seen, when
// set, records what Start was asked; wait, when set, is the runner's exit.
type stubEnvironment struct {
	placement launch.Placement
	seen      *stubSpawn
	wait      func() error
}

// stubEnvAt is a stubEnvironment whose project root is dir, on the host.
func stubEnvAt(dir string, seen *stubSpawn) stubEnvironment {
	return stubEnvironment{placement: launch.Placement{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: dir}})}, seen: seen}
}

// stubSpawn records the runner starts a stubEnvironment saw.
type stubSpawn struct {
	mu       sync.Mutex
	calls    int
	backend  string
	spawnEnv map[string]string
}

func (e stubEnvironment) Placement() launch.Placement { return e.placement }
func (stubEnvironment) Listen() present.Listen        { return present.Listen{} }
func (e stubEnvironment) Start(_ context.Context, r isolation.RunnerRequest) (*isolation.RunnerHandle, error) {
	if e.seen != nil {
		e.seen.mu.Lock()
		e.seen.calls++
		e.seen.backend = r.Engine
		e.seen.spawnEnv = r.Env
		e.seen.mu.Unlock()
	}
	wait := e.wait
	if wait == nil {
		wait = func() error { return nil }
	}
	return &isolation.RunnerHandle{Kill: func() {}, Wait: wait}, nil
}
func (stubEnvironment) Interactive(context.Context, isolation.RunnerRequest) (isolation.Interactive, error) {
	return isolation.Interactive{}, nil
}
func (stubEnvironment) Describe() isolation.Description { return isolation.Description{} }
func (stubEnvironment) Cleanup() error                  { return nil }

// stubPrepareEnvironment swaps the cells adapter's isolation seam for one
// that records a fatal ClassIsolation finding for the members (harps) in
// failFor — simulating exactly what isolation records when an
// explicitly-requested container can't be satisfied — and always returns a
// host environment over the request's project (the degrade chain never
// blocks). Restores the real seam on cleanup.
func stubPrepareEnvironment(t *testing.T, failFor map[string]bool, seen ...*stubSpawn) {
	var rec *stubSpawn
	if len(seen) > 0 {
		rec = seen[0]
	}
	t.Helper()
	prev := prepareEnvironment
	prepareEnvironment = func(_ context.Context, req launch.CellRequest, _ isolation.Spec) (isolation.Environment, error) {
		if agentID := req.Identity.Harp; failFor[agentID] {
			strictness.Fail(report.KindIsolation,
				"install/build the agent image and start the container runtime (docker/podman), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox",
				"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): agent image absent", agentID)
		}
		return stubEnvAt(req.ProjectRoot, rec), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
}

// TestIsolationGateErr pins the gate's decision table directly: only strict-mode
// ClassIsolation findings fail a member; degraded mode and foreign classes pass.
func TestIsolationGateErr(t *testing.T) {
	isoFinding := report.Finding{
		Kind:   report.KindIsolation,
		Text:   "container isolation was requested but could not start",
		Remedy: "start the container runtime, or pass --degraded",
	}

	t.Run("strict + isolation finding → member-fatal error with finding and fix", func(t *testing.T) {
		resetStrictness(t)
		err := isolationGateErr(strictness.Mode{}, []report.Finding{isoFinding})
		require.Error(t, err)
		assert.Contains(t, err.Error(), isoFinding.Text)
		assert.Contains(t, err.Error(), isoFinding.Remedy)
		fix, _ := clifmt.RemedyOf(err)
		assert.Equal(t, isoFinding.Remedy, fix, "a single finding's remedy reaches the error envelope")
	})

	t.Run("strict + several isolation findings → every finding's fix is shown", func(t *testing.T) {
		resetStrictness(t)
		other := report.Finding{
			Kind:   report.KindIsolation,
			Text:   "worktree isolation was requested but the workspace could not be created",
			Remedy: "commit or stash local changes, or pass --degraded",
		}
		err := isolationGateErr(strictness.Mode{}, []report.Finding{isoFinding, other})
		require.Error(t, err)
		assert.Contains(t, err.Error(), clifmt.FixLine("    ", isoFinding.Remedy))
		assert.Contains(t, err.Error(), clifmt.FixLine("    ", other.Remedy))
	})

	t.Run("strict + no findings → nil", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(strictness.Mode{}, nil))
	})

	t.Run("strict + non-isolation findings only → nil (not this gate's class)", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(strictness.Mode{}, []report.Finding{
			{Kind: report.KindSync, Text: "sync failed"},
		}))
	})

	t.Run("degraded → nil even with findings", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(strictness.Mode{Degraded: true}, []report.Finding{isoFinding}))
	})
}
