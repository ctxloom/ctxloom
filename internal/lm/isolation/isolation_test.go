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

func (f failingPolicy) Name() string { return f.name }
func (failingPolicy) PrepareWorkspace(context.Context, string, string) (Workspace, error) {
	return nil, errors.New("agent image absent")
}
func (failingPolicy) SpawnClient(string, string, int, Workspace, map[string]string) (pb.Client, error) {
	return nil, errors.New("unused: the chain degrades before spawn")
}
func (failingPolicy) StartRunner(context.Context, string, string, int, Workspace, map[string]string) (*RunnerHandle, error) {
	return nil, errors.New("unused: the chain degrades before spawn")
}

// passingPolicy is a test Policy that always prepares a trivial workspace (the
// project dir, via None); its Name is configurable so a chain can place a
// SUCCEEDING non-container tier (e.g. a bare worktree) right after a failing
// container tier — the shape that exercises a lost-CONTAINER-boundary degrade
// which still yields a workspace. SpawnClient is never reached (prepareChain
// stops at the first success).
type passingPolicy struct{ name string }

func (p passingPolicy) Name() string { return p.name }
func (passingPolicy) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	return None{}.PrepareWorkspace(ctx, projectDir, agentID)
}
func (passingPolicy) SpawnClient(string, string, int, Workspace, map[string]string) (pb.Client, error) {
	return nil, errors.New("unused: prepareChain stops at the first success")
}
func (passingPolicy) StartRunner(context.Context, string, string, int, Workspace, map[string]string) (*RunnerHandle, error) {
	return nil, errors.New("unused: prepareChain stops at the first success")
}

// TestNone_IsHostIdentical pins the None policy to today's host behaviour: the
// workspace IS the project directory and cleanup is a noop. This is the
// behaviour-identity contract Step 0.2 must preserve.
func TestNone_IsHostIdentical(t *testing.T) {
	p := None{}
	assert.Equal(t, "none", p.Name())

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
	factory := FactoryForWorkspace(None{}, ws, nil)
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
		p := chainFor(axes, "claude-code", ImageConfig{})[0]
		assert.IsType(t, None{}, p, "axes %+v resolve to None", axes)
	}
	// Unknown axis values still degrade the POLICY to the axis defaults (a bad
	// runtime ALSO records a fatal finding, asserted elsewhere; the returned lead
	// policy is unchanged).
	assert.IsType(t, None{}, chainFor(Axes{Workspace: "podracer", Runtime: "hyperdrive"}, "claude-code", ImageConfig{})[0],
		"unknown axis values degrade to None")
	// Independence: an unknown RUNTIME never drops a requested worktree.
	assert.IsType(t, Worktree{}, chainFor(Axes{Workspace: WorkspaceWorktree, Runtime: "hyperdrive"}, "claude-code", ImageConfig{})[0],
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
		assert.NotEmpty(t, strictness.All(),
			"--degraded is the escape hatch: warn, RECORD, then host — it suppresses fatality, not recording")
	})

	t.Run("recognized and empty axis values are silent", func(t *testing.T) {
		resetStrictness(t)
		warnUnknownAxes(Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainerRootless})
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
		assert.NotEmpty(t, strictness.All(),
			"--degraded still records the finding; what it declines to do is abort on it")
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
//
// It answers rt for EVERY demanded ownership, which deliberately bypasses the
// ownership filter — that filter lives inside the real SelectRuntime, so tests
// that exercise it drive the runtimeCandidates seam instead (see
// stubRuntimeCandidates) and leave this seam alone.
func stubRuntimeProbe(t *testing.T, rt Runtime) {
	t.Helper()
	prev := selectRuntimeProbe
	selectRuntimeProbe = func(string, RuntimeAxis) Runtime { return rt }
	t.Cleanup(func() { selectRuntimeProbe = prev })
}

// stubRuntimeCandidates replaces the candidate table the REAL SelectRuntime
// walks, so the ownership filter itself runs under test. Each entry pairs an
// available runtime with the ownership its probe reports — the pairing the
// production table derives from dockerIsRootless()/podmanIsRootless(), which
// shell out and would otherwise make every ownership assertion a report on
// whatever daemon this machine happens to run.
func stubRuntimeCandidates(t *testing.T, cands ...runtimeCandidate) {
	t.Helper()
	prev := runtimeCandidates
	runtimeCandidates = func() []runtimeCandidate { return cands }
	t.Cleanup(func() { runtimeCandidates = prev })
}

// ownedBy builds a candidate whose probe reports an available runtime named
// name owned in mode owns.
func ownedBy(name string, owns RuntimeAxis) runtimeCandidate {
	rt := fakeRuntime{name: name, binary: name, available: true}
	return runtimeCandidate{name: name, probe: func() (Runtime, RuntimeAxis) { return rt, owns }}
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

		chain := chainFor(Axes{Workspace: WorkspaceShared, Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})
		require.Len(t, chain, 1)
		assert.IsType(t, None{}, chain[0], "no runtime → the container tier never enters the chain")

		findings := strictness.All()
		require.Len(t, findings, 1, "runtime-unreachable on an explicit container request is exactly one fatal finding")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "no container runtime is available with that ownership")
		assert.Contains(t, findings[0].Message, string(RuntimeContainerRootless),
			"the finding must name the runtime axis that was actually demanded")
		assert.Contains(t, findings[0].FixIt, "--degraded", "the fix-it must name the escape hatch")
	})

	t.Run("strict {worktree,container}: the finding fires but the worktree survives", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeProbe(t, Host{})

		chain := chainFor(Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})
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

		chain := chainFor(Axes{Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})
		require.Len(t, chain, 1)
		assert.IsType(t, None{}, chain[0])
		assert.NotEmpty(t, strictness.All(),
			"the host degrade is the accepted outcome; the finding is still recorded")
	})
}

// TestSelectRuntime_NoProductionPathAcceptsASilentSubstitution pins that
// an explicit runtime preference that is unknown
// or unavailable is silently replaced by auto-detection, so "a user who
// configured podman can get docker with no diagnostic". SelectRuntime's
// fall-through is real, but no production path reaches it with a preference to
// lose. There are exactly two ways a preference can be expressed:
//
//   - the RUN path (chainFor) does not accept one at all — it probes with the
//     empty auto-detect preference, so there is nothing to silently substitute;
//   - `container build --runtime` does, and selectBuildRuntime REFUSES the
//     substitution loudly rather than building into a daemon the user never
//     asked for.
//
// This pins the first half, which nothing else asserts: wiring a configured
// runtime preference into the run path turns this red, forcing whoever does it
// to answer the diagnostic question rather than inheriting the silent
// fall-through.
func TestSelectRuntime_NoProductionPathAcceptsASilentSubstitution(t *testing.T) {
	type probed struct {
		prefer string
		want   RuntimeAxis
	}
	var seen []probed
	prev := selectRuntimeProbe
	selectRuntimeProbe = func(prefer string, want RuntimeAxis) Runtime {
		seen = append(seen, probed{prefer, want})
		return Docker{}
	}
	t.Cleanup(func() { selectRuntimeProbe = prev })

	for _, axes := range []Axes{
		{Workspace: WorkspaceShared, Runtime: RuntimeContainerRootless},
		{Workspace: WorkspaceWorktree, Runtime: RuntimeContainerRootful},
	} {
		chainFor(axes, "claude-code", ImageConfig{})
	}

	require.Len(t, seen, 2, "the container tiers must actually probe for a runtime")
	for _, p := range seen {
		assert.Empty(t, p.prefer,
			"the run path must probe with auto-detect; a preference expressed here would be silently substituted by SelectRuntime's fall-through, which only selectBuildRuntime guards against")
	}
	// The OWNERSHIP demand, by contrast, must ride through: it is the whole
	// point of the two container values, and a run that probes with anything
	// but the axis it was asked for can be handed the other mode.
	assert.Equal(t, RuntimeContainerRootless, seen[0].want)
	assert.Equal(t, RuntimeContainerRootful, seen[1].want)
}

// TestIsContainerPolicyName_AgreesWithEveryPolicysOwnName pins that the
// predicate every "did we keep the container boundary?" check funnels through
// must agree with what each policy actually calls itself. It used to match on
// its own copies of the two name literals, so a rename on either base would
// have silently reclassified a container run as an unsandboxed one — the
// prepareChain warning downgraded to a generic degrade line, and the run path
// reporting a satisfied container request as dropped. Asserting against the
// policies' OWN Name() keeps the agreement checkable no matter where the
// strings live.
func TestIsContainerPolicyName_AgreesWithEveryPolicysOwnName(t *testing.T) {
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}

	assert.True(t, IsContainerPolicyName(NewContainerFor(rt, "mock").WithImage("img").Name()),
		"the host-base container policy is container-backed")
	assert.True(t, IsContainerPolicyName(NewContainerWorktreeFor(rt, "claude-code", ImageConfig{}, nil).Name()),
		"the worktree-base container policy is container-backed")
	assert.True(t, IsContainerPolicyName(Container{}.Name()),
		"a bare Container (nil base) still reports a container-backed name")

	assert.False(t, IsContainerPolicyName(None{}.Name()), "the host policy is not a container boundary")
	assert.False(t, IsContainerPolicyName(NewWorktree(nil, "claude-code").Name()),
		"a host worktree is a workspace boundary, never a container one")
	assert.False(t, IsContainerPolicyName(""), "an empty name is never a container boundary")
}

// TestNonePrepareWorkspace_CannotFail REFUTES a finding, which flagged
// prepareChain's trailing `ws, _ := None{}.PrepareWorkspace(...)` as discarding
// "the one place a total failure would be invisible". None.PrepareWorkspace
// returns a literal nil error on every path — the discard is statically
// unreachable, not merely unlikely — and there is no lower tier to degrade
// into, so handling it could only produce an unreachable branch. The row's own
// remedy is therefore not implementable: there is nothing an error path could
// do that returning the project dir does not already do.
//
// That "None never fails" is the whole fault-tolerance floor the Policy
// contract rests on (chainFor always terminates in None; prepareChain's
// trailing call is the defensive fallback for an empty or all-failing chain).
// So pin the property instead: if None ever grows a failure mode, this goes
// red and the discard must be revisited.
func TestNonePrepareWorkspace_CannotFail(t *testing.T) {
	ctx := context.Background()
	for _, dir := range []string{"", "/proj", "/does/not/exist", "\x00not-a-path"} {
		ws, err := None{}.PrepareWorkspace(ctx, dir, "agent-a")
		require.NoError(t, err, "None is the fault-tolerant floor: it never fails to prepare (dir %q)", dir)
		require.NotNil(t, ws)
		assert.Equal(t, dir, ws.Dir())
		assert.NoError(t, ws.Cleanup(), "and its teardown is a noop that never fails")
	}

	// The same holds through the seam prepareChain actually reaches it by: an
	// all-failing chain still yields a workspace rather than a nil one.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	policy, ws := prepareChain(cancelled, []Policy{failingPolicy{name: "worktree"}}, "/proj", "agent-a")
	assert.Equal(t, None{}.Name(), policy.Name())
	require.NotNil(t, ws)
	assert.Equal(t, "/proj", ws.Dir())
}

// TestImageConfigZeroValue_DisablesDevcontainerDetectionSilently REFUTES
// a finding that called ImageConfig's zero value "self-contradictory"
// (NoDevcontainerBase false says auto-detect ON while AppRoot "" forces it OFF)
// and asked for a diagnostic. Both halves are wrong:
//
//   - It is not a contradiction but a DOCUMENTED equivalence, stated at both
//     sites — ImageConfig.AppRoot's own field doc ('"" disables auto-detection
//     (same effect as NoDevcontainerBase)') and resolveDevBase's ("an empty
//     appRoot ... means 'no auto-detect', never an error").
//   - There is nothing to diagnose. An empty AppRoot means no project root is
//     known, so there is no directory to resolve .devcontainer/devcontainer.json
//     AGAINST; detection is impossible rather than skipped. The zero value
//     arises for callers that legitimately never learned a root, and warning on
//     every one of them would be noise on a path that has no alternative.
//     Where a config-load failure IS the cause, the CLI already names the gap
//     (containerCheckConfigGap in cli/container_cmd.go).
//
// Pinning the silence, so re-introducing the requested diagnostic goes red.
func TestImageConfigZeroValue_DisablesDevcontainerDetectionSilently(t *testing.T) {
	buf := captureWarnings(t)

	stage, err := resolveDevBase("", ImageConfig{}.NoDevcontainerBase, "")
	require.NoError(t, err, "an unknown project root is never an error")
	assert.Nil(t, stage, "and never resolves a base")

	optedOut, err := resolveDevBase("/some/root", true, "")
	require.NoError(t, err)
	assert.Nil(t, optedOut)
	assert.Empty(t, buf.String(),
		"an empty AppRoot is the documented equivalent of the explicit opt-out — no root means nothing to detect against, so there is nothing to report")
}

// TestImageOverrideAndBaseImageAreOppositeConcepts PARTIALLY refutes
// a finding that claimed ImageConfig and ImageBuildOptions "duplicate 6 of the
// same concepts under different names". Five are genuinely the same and share
// their names exactly (BaseContainerfile, AppRoot, NoDevcontainerBase,
// DevcontainerService, Engines). The sixth pairing the row implies —
// ImageConfig.Image against ImageBuildOptions.BaseImage — is not a rename of
// one concept but two OPPOSITE ones, which is why they were never unified:
//
//   - ImageConfig.Image is a prebuilt override, run AS-IS and NEVER built: it
//     suppresses the local recipe entirely (engineInstall is cleared) so an
//     absent override degrades rather than triggering a build.
//   - ImageBuildOptions.BaseImage is a base to BUILD ONTO: it produces an
//     overlay build source that layers ctxloom on top.
//
// A shared type that merged them would let a user's run-as-is image be built
// onto, or a build base be launched unbuilt. Pinning the divergence.
func TestImageOverrideAndBaseImageAreOppositeConcepts(t *testing.T) {
	rt := fakeRuntime{name: "docker", binary: "false", available: true}

	c := containerFor(rt, "kiro", ImageConfig{Image: "my-registry/my-kiro:v2"})
	assert.Equal(t, "my-registry/my-kiro:v2", c.image, "the override IS the image, verbatim")
	sources, _, _ := c.containerBuildSources("")
	assert.Empty(t, sources, "an isolation_images override has NO build recipe — the user owns its lifecycle")

	overlay := buildSources(engineContainerSpecFor("kiro"), buildSourcesOptions{baseOverride: "my-registry/my-kiro:v2"})
	require.Len(t, overlay, 1, "the same string as a BaseImage is a base to build onto, not an image to run")
	assert.Contains(t, string(overlay[0].containerfile), "FROM my-registry/my-kiro:v2\n")
	assert.Contains(t, string(overlay[0].containerfile), "COPY ctxloom /usr/local/bin/ctxloom\n",
		"a build base gets ctxloom layered onto it; a run-as-is override never would")
}

// TestParseWorkspaceAxis is the workspace vocabulary's own contract: the two
// members round-trip, empty passes through as "this level said nothing", and
// everything else is refused naming the legal set.
//
// The empty case is load-bearing and not a formality: each caller's layering
// resolves silence differently (a delegated child defaults to its own
// worktree, a top-level run to the shared checkout), so the parser must hand
// silence back unchanged rather than picking one of them.
func TestParseWorkspaceAxis(t *testing.T) {
	for _, member := range WorkspaceNames() {
		got, err := ParseWorkspaceAxis(member)
		require.NoError(t, err, "%q is a declared member", member)
		assert.Equal(t, WorkspaceAxis(member), got)
	}
	require.Equal(t, []string{"none", "worktree"}, WorkspaceNames())

	got, err := ParseWorkspaceAxis("")
	require.NoError(t, err, "unset is not an error")
	assert.Equal(t, WorkspaceAxis(""), got)

	for _, bad := range []string{"wroktree", "worktree ", "Worktree", "shared", "host"} {
		got, err := ParseWorkspaceAxis(bad)
		require.Error(t, err, "%q is not a member", bad)
		assert.Equal(t, WorkspaceAxis(""), got)
		assert.Contains(t, err.Error(), bad)
		assert.Contains(t, err.Error(), "none|worktree")
		// The refusal exists because the FALLBACK is worse than nothing:
		// WantsWorktree reads anything unrecognized as the shared checkout.
		assert.False(t, Axes{Workspace: WorkspaceAxis(bad)}.WantsWorktree(),
			"an unparsed spelling really would have read as the shared checkout")
	}
}
