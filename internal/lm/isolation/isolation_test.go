package isolation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetStrictness restores pristine strict-mode state for a test and registers
// cleanup, so the package-global finding collector never bleeds between tests
// (mirrors strictness_test.go's resetForTest).
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// failingPolicy is a test Policy whose Name is configurable and whose
// PrepareWorkspace always fails — a stand-in for a tier that cannot launch
// (a container whose image is absent / probe failed / auth unresolvable, or a
// worktree that cannot be added). SpawnClient is never reached: the chain
// always degrades past a failing policy to None.
type failingPolicy struct{ name string }

func (f failingPolicy) Name() string      { return f.name }
func (failingPolicy) Approvals() Approvals { return ApprovalsBypass }
func (failingPolicy) PrepareWorkspace(context.Context, string, string) (Workspace, error) {
	return nil, errors.New("agent image absent")
}
func (failingPolicy) SpawnClient(string, string, int, Workspace) (pb.Client, error) {
	return nil, errors.New("unused: the chain degrades before spawn")
}

// passingPolicy is a test Policy that always prepares a trivial workspace (the
// project dir, via None); its Name is configurable so a chain can place a
// SUCCEEDING non-container tier (e.g. a bare worktree) right after a failing
// container tier — the shape that exercises a lost-CONTAINER-boundary degrade
// which still yields a workspace. SpawnClient is never reached (prepareChain
// stops at the first success).
type passingPolicy struct{ name string }

func (p passingPolicy) Name() string      { return p.name }
func (passingPolicy) Approvals() Approvals { return ApprovalsPrompt }
func (passingPolicy) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	return None{}.PrepareWorkspace(ctx, projectDir, agentID)
}
func (passingPolicy) SpawnClient(string, string, int, Workspace) (pb.Client, error) {
	return nil, errors.New("unused: prepareChain stops at the first success")
}

// TestNone_IsHostIdentical pins the None policy to today's host behaviour: the
// workspace IS the project directory, cleanup is a noop, and approvals stay
// Prompt. This is the behaviour-identity contract Step 0.2 must preserve.
func TestNone_IsHostIdentical(t *testing.T) {
	p := None{}
	assert.Equal(t, "none", p.Name())
	assert.Equal(t, ApprovalsPrompt, p.Approvals(), "none keeps the engine's in-tool approval prompt")

	ws, err := p.PrepareWorkspace(context.Background(), "/project/root", "member-a")
	require.NoError(t, err, "None never fails to prepare a workspace")
	assert.Equal(t, "/project/root", ws.Dir(), "none workspace is the live project directory")
	assert.NoError(t, ws.Cleanup(), "none cleanup is a noop")
	// Cleanup is idempotent — safe to call more than once.
	assert.NoError(t, ws.Cleanup())
}

// TestFactoryForWorkspace_BindsPolicy proves the bridge yields a usable
// pb.ClientFactory (the seam the fan-out injects). The None factory's spawn body
// is verbatim pb.NewSelfInvokingClientForLabel — the same call
// pb.DefaultClientFactory makes — so a live spawn is left to the operations /
// conformance suites; here we assert the bridge wires a non-nil factory.
func TestFactoryForWorkspace_BindsPolicy(t *testing.T) {
	ws, err := None{}.PrepareWorkspace(context.Background(), "/project/root", "m")
	require.NoError(t, err)
	factory := FactoryForWorkspace(None{}, ws)
	require.NotNil(t, factory, "the bridge must produce a client factory")
}

// TestResolve_DefaultsAndDegrades: empty/"none"/"host" axes resolve to None;
// unknown axis values degrade to that axis's default (fault tolerance) rather
// than failing.
func TestResolve_DefaultsAndDegrades(t *testing.T) {
	// An unknown RUNTIME value now records a fatal finding (see
	// TestWarnUnknownAxes_RuntimeFatal_WorkspaceBenign); reset so it never bleeds.
	resetStrictness(t)
	for _, axes := range []Axes{
		{},
		{Workspace: WorkspaceShared, Runtime: RuntimeHost},
	} {
		p := Resolve(axes, "claude-code", ImageConfig{})
		assert.IsType(t, None{}, p, "axes %+v resolve to None", axes)
	}
	// Unknown axis values still degrade the POLICY to the axis defaults (a bad
	// runtime ALSO records a fatal finding, asserted elsewhere; the returned lead
	// policy is unchanged).
	assert.IsType(t, None{}, Resolve(Axes{Workspace: "podracer", Runtime: "hyperdrive"}, "claude-code", ImageConfig{}),
		"unknown axis values degrade to None")
	// Independence: an unknown RUNTIME never drops a requested worktree.
	assert.IsType(t, Worktree{}, Resolve(Axes{Workspace: WorkspaceWorktree, Runtime: "hyperdrive"}, "claude-code", ImageConfig{}),
		"an unknown runtime axis degrades alone; the workspace axis survives")
}

// TestWarnUnknownAxes_RuntimeFatal_WorkspaceBenign pins the per-axis severity
// split: a typo'd RUNTIME value would silently land the run UNSANDBOXED on the
// host, so it is a fatal ClassIsolation finding the choke owner aborts on unless
// --degraded; a typo'd WORKSPACE value degrades to the shared project dir (a
// convenience axis, never a boundary), so it stays a plain warn-and-continue.
func TestWarnUnknownAxes_RuntimeFatal_WorkspaceBenign(t *testing.T) {
	t.Run("strict: an unknown RUNTIME axis is one fatal isolation finding", func(t *testing.T) {
		resetStrictness(t)
		warnUnknownAxes(Axes{Runtime: "hyperdrive"})

		findings := strictness.All()
		require.Len(t, findings, 1, "a typo'd runtime that would land on the host is fatal")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "NOT sandboxed", "the finding must flag the silent downgrade")
		assert.Contains(t, findings[0].FixIt, "--degraded", "the fix-it must name the escape hatch")
	})

	t.Run("strict: an unknown WORKSPACE axis warns but records nothing", func(t *testing.T) {
		resetStrictness(t)
		warnUnknownAxes(Axes{Workspace: "podracer"})
		assert.Empty(t, strictness.All(),
			"a typo'd workspace axis degrades to the shared dir — benign, never fatal")
	})

	t.Run("degraded: an unknown RUNTIME axis records nothing", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		warnUnknownAxes(Axes{Runtime: "hyperdrive"})
		assert.Empty(t, strictness.All(), "--degraded is the escape hatch: warn, then host")
	})

	t.Run("recognized and empty axis values are silent", func(t *testing.T) {
		resetStrictness(t)
		warnUnknownAxes(Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainer})
		warnUnknownAxes(Axes{}) // the ambient default must never fire
		assert.Empty(t, strictness.All())
	})
}

// TestPrepareChain_RequestedContainerDegrade_FatalUnlessDegraded pins the
// fail-loudly contract for an EXPLICITLY-requested container that can't be
// satisfied: prepareChain still degrades to the host (the run always gets a
// workspace), but in strict mode it records a fatal ClassIsolation finding for
// the choke owner to abort on, while --degraded downgrades it to today's plain
// host-degrade (no finding). A non-container (workspace-axis) degrade must never
// record such a finding — only a LOST CONTAINER BOUNDARY is fatal.
func TestPrepareChain_RequestedContainerDegrade_FatalUnlessDegraded(t *testing.T) {
	// The chain chainFor builds ONLY for an explicitly-requested container: a
	// container tier that fails to prepare (image absent / probe / auth),
	// degrading to None.
	containerChain := []Policy{failingPolicy{name: (Container{}).Name()}, None{}}

	t.Run("strict: records one fatal isolation finding and still degrades to the host", func(t *testing.T) {
		resetStrictness(t)
		// The mark the run path's post-Prepare gate would anchor at, captured
		// BEFORE the degrade so Since(mark) proves the finding lands inside the
		// gate's scan window (not merely that some finding exists somewhere).
		mark := strictness.Checkpoint()

		policy, ws := prepareChain(context.Background(), containerChain, "/project", "agent-a")
		require.NotNil(t, ws, "the run always gets a workspace — the degrade never blocks the LLM")
		assert.IsType(t, None{}, policy, "the boundary is lost, so the workspace falls back to the host")

		findings := strictness.All()
		require.Len(t, findings, 1, "a requested container that can't launch is exactly one fatal finding")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class, "classified so the abort reads [isolation]")
		assert.Contains(t, findings[0].Message, "NOT sandboxed", "the message must flag the lost boundary")
		require.NotEmpty(t, findings[0].FixIt, "the finding must carry a fix-it hint")
		assert.Contains(t, findings[0].FixIt, "--degraded", "the fix-it must name the escape hatch")

		// The finding lands inside the window a choke owner's post-Prepare gate
		// scans (strictness.Since(mark)). The class→exit-code-3 ABORT mapping
		// itself is pinned in the cli package's failOnFindings test — this package
		// cannot import cli — so here we assert only that the finding is visible to
		// that gate window, not the exit code.
		gated := strictness.Since(mark)
		require.Len(t, gated, 1, "the finding falls inside the choke owner's Since(mark) gate window")
		assert.Equal(t, strictness.ClassIsolation, gated[0].Class)
	})

	t.Run("a ContainerWorktree that degrades to a bare worktree is a lost-boundary finding", func(t *testing.T) {
		resetStrictness(t)

		// The {worktree, container} chain: the container-worktree tier fails to
		// launch and degrades to the SURVIVING bare worktree. The container
		// boundary is lost even though the workspace axis is preserved, so it is
		// fatal — a container→non-container transition, not the benign
		// worktree→none workspace-axis degrade.
		chain := []Policy{failingPolicy{name: "container-worktree"}, passingPolicy{name: (Worktree{}).Name()}, None{}}
		policy, ws := prepareChain(context.Background(), chain, "/project", "agent-a")
		require.NotNil(t, ws)
		assert.Equal(t, "worktree", policy.Name(), "the requested worktree survives the lost container boundary")

		findings := strictness.All()
		require.Len(t, findings, 1, "dropping the container half of container-worktree is exactly one fatal finding")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "NOT sandboxed", "the message must flag the lost boundary")
	})

	t.Run("degraded: records nothing — the host degrade is the accepted outcome", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		policy, ws := prepareChain(context.Background(), containerChain, "/project", "agent-a")
		require.NotNil(t, ws)
		assert.IsType(t, None{}, policy)
		assert.Empty(t, strictness.All(), "--degraded is the escape hatch: no finding, just the host degrade")
	})

	t.Run("a workspace-axis degrade (worktree→none) is not an isolation finding", func(t *testing.T) {
		resetStrictness(t)

		workspaceChain := []Policy{failingPolicy{name: (Worktree{}).Name()}, None{}}
		policy, _ := prepareChain(context.Background(), workspaceChain, "/project", "agent-a")
		assert.IsType(t, None{}, policy)
		assert.Empty(t, strictness.All(),
			"a lost worktree degrades gracefully — only a lost CONTAINER boundary is fatal")
	})
}

// stubRuntimeProbe swaps chainFor's runtime probe for one returning rt, and
// restores the real SelectRuntime on cleanup. Hermetic: the real probe shells
// out to docker/podman on the host.
func stubRuntimeProbe(t *testing.T, rt ContainerRuntime) {
	t.Helper()
	prev := selectRuntimeProbe
	selectRuntimeProbe = func(string) ContainerRuntime { return rt }
	t.Cleanup(func() { selectRuntimeProbe = prev })
}

// TestChainFor_NoRuntime_FatalUnlessDegraded pins chainFor's no-runtime fatal
// path hermetically (via the selectRuntimeProbe seam): an EXPLICITLY-requested
// container with no reachable runtime records a fatal ClassIsolation finding in
// strict mode — the choke owner aborts on it — while the chain still degrades
// so the run gets a workspace (never blocks). The workspace axis must survive
// the runtime-axis degrade untouched.
func TestChainFor_NoRuntime_FatalUnlessDegraded(t *testing.T) {
	t.Run("strict {none,container}: one fatal isolation finding; chain degrades to None", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, Host{})

		chain := chainFor(Axes{Workspace: WorkspaceShared, Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
		require.Len(t, chain, 1)
		assert.IsType(t, None{}, chain[0], "no runtime → the container tier never enters the chain")

		findings := strictness.All()
		require.Len(t, findings, 1, "runtime-unreachable on an explicit container request is exactly one fatal finding")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "no container runtime is available")
		assert.Contains(t, findings[0].FixIt, "--degraded", "the fix-it must name the escape hatch")
	})

	t.Run("strict {worktree,container}: the finding fires but the worktree survives", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, Host{})

		chain := chainFor(Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
		require.NotEmpty(t, chain)
		assert.IsType(t, Worktree{}, chain[0], "the runtime axis degrades ALONE; the requested worktree stays")

		findings := strictness.All()
		require.Len(t, findings, 1)
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "keeping the worktree", "the message must say the worktree survived")
	})

	t.Run("degraded: no finding — the host degrade is the accepted outcome", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		stubRuntimeProbe(t, Host{})

		chain := chainFor(Axes{Runtime: RuntimeContainer}, "claude-code", ImageConfig{})
		require.Len(t, chain, 1)
		assert.IsType(t, None{}, chain[0])
		assert.Empty(t, strictness.All())
	})
}

// TestApprovals_String renders the approvals axis for diagnostics.
func TestApprovals_String(t *testing.T) {
	assert.Equal(t, "prompt", ApprovalsPrompt.String())
	assert.Equal(t, "bypass", ApprovalsBypass.String())
}
