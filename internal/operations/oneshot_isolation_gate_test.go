package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetStrictness restores pristine strict-mode state for a test and registers
// cleanup, so the package-global finding collector never bleeds between tests
// (mirrors isolation_test.go's helper of the same name).
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// stubWorkspace is a minimal isolation.Workspace for the prepareIsolation stub.
type stubWorkspace struct{ dir string }

func (w stubWorkspace) Dir() string    { return w.dir }
func (w stubWorkspace) Cleanup() error { return nil }

// stubPolicy is a minimal isolation.Policy whose SpawnClient mints a canned
// pb.Client per spawn — so a member that PASSES the gate still never spawns a
// real plugin subprocess (and parallel members never share one client). Name
// reports "none" so runResolvedAgent stays on the SkipSetup/lead-fragment path
// (no managed-config assembly).
type stubPolicy struct{ mk func() pb.Client }

func (stubPolicy) Name() string                   { return isolation.None{}.Name() }
func (stubPolicy) Approvals() isolation.Approvals { return isolation.ApprovalsPrompt }
func (p stubPolicy) PrepareWorkspace(_ context.Context, projectDir, _ string) (isolation.Workspace, error) {
	return stubWorkspace{dir: projectDir}, nil
}
func (p stubPolicy) SpawnClient(string, string, int, isolation.Workspace) (pb.Client, error) {
	return p.mk(), nil
}

// stubPrepareIsolation swaps runResolvedAgent's isolation.Prepare seam for one
// that records a fatal ClassIsolation finding for the agentIDs in failFor —
// simulating exactly what prepareChain/chainFor do when an explicitly-requested
// container can't be satisfied — and always returns a host workspace (the
// degrade chain never blocks). Restores the real Prepare on cleanup.
func stubPrepareIsolation(t *testing.T, failFor map[string]bool, mk func() pb.Client) {
	t.Helper()
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, _ isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, agentID string) (isolation.Policy, isolation.Workspace) {
		if failFor[agentID] {
			strictness.Fail(strictness.ClassIsolation,
				"install/build the agent image and start the container runtime (docker/podman), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox",
				"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): agent image absent", agentID)
		}
		return stubPolicy{mk: mk}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })
}

// TestRunResolvedAgent_ContainerDegradeFailsMemberInStrict pins the fail-loudly
// member gate: when isolation.Prepare records a ClassIsolation finding (an
// explicitly-requested container degraded toward the bare host), strict mode
// FAILS THE MEMBER — the error carries the finding text — instead of running it
// unsandboxed with forced bypass. In degraded mode (recording disabled) the
// member proceeds on the degraded host workspace as before.
func TestRunResolvedAgent_ContainerDegradeFailsMemberInStrict(t *testing.T) {
	t.Run("strict: the member fails with the finding text; no engine runs", func(t *testing.T) {
		resetStrictness(t)
		stub := &stubClient{out: "should never run"}
		stubPrepareIsolation(t, map[string]bool{"m1": true}, func() pb.Client { return stub })

		res, err := runResolvedAgent(context.Background(), resolvedRunRequest{
			Task:    "t",
			WorkDir: t.TempDir(),
			Label:   "claude-fast",
			Backend: "claude-code",
			Axes:    isolation.Axes{Runtime: isolation.RuntimeContainer},
			AgentID: "m1",
			Factory: nil, // the isolation path — the seam under test
		})
		require.Error(t, err, "a member whose requested container degraded must FAIL in strict mode")
		assert.Nil(t, res)
		assert.Contains(t, err.Error(), "NOT sandboxed", "the error must carry the finding text")
		assert.Contains(t, err.Error(), "--degraded", "the error must name the escape hatch")
		assert.Nil(t, stub.gotReq, "the engine must never have run")
	})

	t.Run("degraded: the member proceeds on the degraded host workspace", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		stub := &stubClient{out: "ran on host"}
		stubPrepareIsolation(t, map[string]bool{"m1": true}, func() pb.Client { return stub })

		res, err := runResolvedAgent(context.Background(), resolvedRunRequest{
			Task:    "t",
			WorkDir: t.TempDir(),
			Label:   "claude-fast",
			Backend: "claude-code",
			Axes:    isolation.Axes{Runtime: isolation.RuntimeContainer},
			AgentID: "m1",
			Factory: nil,
		})
		require.NoError(t, err, "--degraded keeps the old warn-and-continue host fallback")
		assert.Equal(t, "ran on host", res.Output)
	})
}

// TestMapProfiles_ContainerDegradeIsPerMember pins partial success: in a fan
// where SOME members' requested containers can't be satisfied, exactly those
// members become error Parts and every other member still runs — 9-of-10 OK
// still succeeds, but no member is ever silently unsandboxed. The members run
// on parallel goroutines, so this also exercises the serialized
// checkpoint→Prepare→gate window (member windows must not cross-contaminate).
func TestMapProfiles_ContainerDegradeIsPerMember(t *testing.T) {
	resetStrictness(t)
	_, loader := setupContextTestFS(t)
	cfg := mapTestConfig()
	cfg.Profiles.Definitions["c"] = cfg.Profiles.Definitions["a"]
	cfg.Profiles.Definitions["d"] = cfg.Profiles.Definitions["b"]

	stubPrepareIsolation(t, map[string]bool{"b": true, "d": true}, func() pb.Client { return &stubClient{out: "ok"} })

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Members: []string{"a", "b", "c", "d"},
		Task:    "review",
		WorkDir: t.TempDir(),
		Loader:  loader,
		Factory: nil, // the isolation path — every member goes through Prepare
	})

	require.Len(t, parts, 4)
	for i, want := range []struct {
		profile string
		failed  bool
	}{
		{"a", false}, {"b", true}, {"c", false}, {"d", true},
	} {
		assert.Equal(t, want.profile, parts[i].Profile)
		assert.Equal(t, want.failed, parts[i].Failed(),
			"member %q: gate failures must hit exactly the members whose container degraded", want.profile)
		if want.failed {
			assert.Contains(t, parts[i].Err, "NOT sandboxed")
			assert.Contains(t, parts[i].Err, want.profile,
				"the error Part must carry ITS OWN member's finding, not a parallel member's")
		} else {
			assert.Equal(t, "ok", parts[i].Output)
		}
	}
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
		err := isolationGateErr([]strictness.Finding{isoFinding})
		require.Error(t, err)
		assert.Contains(t, err.Error(), isoFinding.Message)
		assert.Contains(t, err.Error(), isoFinding.FixIt)
	})

	t.Run("strict + no findings → nil", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr(nil))
	})

	t.Run("strict + non-isolation findings only → nil (not this gate's class)", func(t *testing.T) {
		resetStrictness(t)
		assert.NoError(t, isolationGateErr([]strictness.Finding{
			{Class: strictness.ClassSync, Message: "sync failed"},
		}))
	})

	t.Run("degraded → nil even with findings", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		assert.NoError(t, isolationGateErr([]strictness.Finding{isoFinding}))
	})
}
