//go:build docker_integration

// The delivery half of the container axis, observed on a container CTXLOOM'S
// OWN isolation built. Run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run TestContainerRun_
//
// WHY THIS FILE EXISTS. J001400's container delivery rows (tests/acceptance,
// through internal/testsupport/containercell) prove that delivery INTO a
// bind-mounted workspace survives a process boundary: bytes, POSIX mode and
// host ownership all come out of the mount intact. What they cannot prove is
// the step before it, because the cell mounts and launches the container
// itself: that ctxloom's OWN container policy points a turn's delivery at the
// mounted workspace rather than at the per-run scratch overlay it mounts over
// the engine's managed-config directory (containerConfigOverlay), which is
// removed at teardown and never copied back out. That residue is stated at the
// scenario in j001400_bundle_distribution.feature; this file closes it.
//
// THE VEHICLE. The cell supplies the hermetic ingredients — the runtime probe
// and its skip vocabulary (dockergate), the statically linked ctxloom, and a
// `FROM scratch` image carrying it at the path the policy execs
// (containercell.EnsureRunPolicyImage) — and the PRODUCT supplies everything
// else: Container.PrepareWorkspace builds the mount plan, SpawnClient starts
// the container and the plugin inside it, and pb.Client.Run drives a real turn
// whose Setup delivers the mock backend's surfaces from INSIDE that container.
// Nothing about the container is the test's own construction.
//
// THE VANTAGE POINT IS MID-TURN, and it is the only honest one. grpc.RunTurn
// calls Cleanup the instant Execute returns and the shared LIFO reversal strips
// the delivered managed section, so a test that stats the workspace after the
// turn observes nothing whether delivery worked or not (the same reason J001400's
// host rows use `spec materialize`, and the same window
// container_mcp_integration_test.go reads .mcp.json through). The mock's
// interactive echo mode (CTXLOOM_MOCK_ECHO_STDIN) blocks the turn on stdin,
// which holds that window open for as long as the host keeps the pipe open —
// and each test then asserts the POST-teardown state too, so "gone afterwards"
// can never be mistaken for "never delivered".
//
// PROFILE VS PLUGIN. The policy is built for the claude-code spec (its auth
// resolver is the one a test can satisfy with an env var, per WithImage's own
// doc) while the plugin that runs inside is `mock` — the only backend compiled
// into ctxloom that delivers surfaces without a vendor CLI in the image. The
// spec decides the MOUNT PLAN (which is what is under test); the plugin
// decides what gets written through it.
package isolation

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// The turn's own payload. The fragment body is several lines long so a
// containment check is a claim about the delivered prose rather than a search
// for one token that could survive truncation.
const (
	runFragmentBody = "RUN-CONTAINER-FRAGMENT-9b12ae the turn's own fragment bytes must reach the file\n" +
		"an engine reads, through the mount ctxloom's own container policy built,\n" +
		"and they must arrive verbatim rather than merely changing its length."
	runSkillBody    = "RUN-CONTAINER-SKILL-4d70c8 the skill package's own bytes, delivered whole."
	runScriptBody   = "#!/bin/sh\necho RUN-CONTAINER-SCRIPT-1f55b3\n"
	runAgentID      = "delivery-itest"
	runTurnDeadline = 3 * time.Minute
)

// TestContainerRun_DeliversIntoTheMountedWorkspace is the positive claim: a
// turn whose plugin runs inside a container ctxloom built delivers its surfaces
// into the BIND-MOUNTED workspace, where the host reads them back through the
// identical-path mount while the turn is still live — bytes, mode and
// ownership — and the turn's own teardown then reverses them.
//
// The container's only route to that path is the mount, so a delivery that
// resolved anywhere container-private (an ephemeral layer, a scratch overlay)
// leaves these assertions red on an absent file. The project dir is checked
// EMPTY before the container starts, so no host-side leftover can stand in.
func TestContainerRun_DeliversIntoTheMountedWorkspace(t *testing.T) {
	rt := containerRunRuntime(t, "the containerized-run delivery proof")
	pol := containerRunPolicy(t, rt)

	projectDir := t.TempDir()
	requireEmptyProject(t, projectDir)

	contextFile := filepath.Join(projectDir, "MOCK_CONTEXT.md")
	skillFile := filepath.Join(projectDir, ".mock", "skills", "reviewer", "SKILL.md")
	scriptFile := filepath.Join(projectDir, ".mock", "skills", "reviewer", "scripts", "run.sh")

	deliverInContainer(t, pol, projectDir, func(Workspace) {
		// The context file is the delivery the turn cannot skip; wait on it
		// rather than on a sleep, then read everything else in the same window.
		requireEventuallyFile(t, contextFile, "the mock context surface")
		requireEventuallyFile(t, skillFile, "the skill package's SKILL.md")
		requireEventuallyFile(t, scriptFile, "the skill package's script")

		body := readHostFile(t, contextFile)
		assert.Contains(t, body, runFragmentBody,
			"the turn's fragment bytes must cross the container boundary verbatim into the file the engine reads")
		assert.Contains(t, body, agent.ManagedContextBegin,
			"the delivered content must sit inside the ctxloom-managed markers")
		assert.Contains(t, readHostFile(t, skillFile), runSkillBody,
			"the skill package's bytes must survive the boundary")
		assert.Equal(t, runScriptBody, readHostFile(t, scriptFile),
			"the skill package's script must survive the boundary byte for byte")

		assertHostMode(t, contextFile, 0o600, "the mock context file")
		assertHostMode(t, skillFile, 0o644, "the skill package's SKILL.md")
		// The exec bit is the one mode that is load-bearing rather than
		// cosmetic, and the 0644 above beside it is the claim that a blanket
		// chmod did not smear it across the package.
		assertHostMode(t, scriptFile, 0o755, "the skill package's script")

		// OWNERSHIP is what a process boundary breaks while bytes and modes
		// come through untouched: a rootful daemon writing through a bind
		// mount leaves byte-identical, mode-identical, ROOT-owned files in the
		// invoking user's tree.
		for _, p := range []string{contextFile, skillFile, scriptFile} {
			require.NoError(t, containercell.AssertOwnedByInvoker(p, filepath.Base(p)))
		}
	})

	// And the turn reversed itself: the shared LIFO Cleanup strips the managed
	// section (removing the file when nothing user-authored remains) and the
	// skills manifest reverts exactly the files it wrote. Asserting this pins
	// the window the observation above ran in — without it, "delivered" and
	// "left behind forever" would be indistinguishable.
	assertGone(t, contextFile, "the mock context file")
	assertGone(t, skillFile, "the delivered SKILL.md")
}

// TestContainerRun_OverlaidConfigDirIsSwallowedByTheScratchOverlay is the
// containerConfigOverlay claim, live: when the delivering surface's directory
// is one of the spec's overlayDirs, the container's write lands in the
// per-run SCRATCH overlay — visible on the host only at the overlay's own host
// side — and NEVER in the bind-mounted project. The scratch root is removed at
// teardown, so that delivery is discarded rather than copied back out.
//
// This is the same run as the test above with ONE knob moved: the spec
// overlays `.mock`, which is where this engine's skills surface writes, exactly
// as claude's `.claude` overlay covers where claude's skills surface writes.
// The mock plugin is the only backend that can deliver hermetically, so the
// overlay is pointed at ITS directory rather than a claude container being
// stood up with no claude in it.
//
// Both halves are asserted in the same window, and both are load-bearing: the
// context file (NOT overlaid) proving this run really did deliver through the
// mount, and the skill package (overlaid) proving the same run's write to an
// overlaid directory never reached the project. Without the first half, an
// assertion that a file is absent from the project would pass just as well for
// a container that wrote nothing at all.
func TestContainerRun_OverlaidConfigDirIsSwallowedByTheScratchOverlay(t *testing.T) {
	rt := containerRunRuntime(t, "the containerized-run config-overlay proof")
	pol := containerRunPolicy(t, rt)
	// The engine's managed-config dir for THIS run: mock's skills surface
	// writes under .mock/skills, so overlaying .mock puts the delivery in the
	// same position claude's .claude/skills delivery is in on a real run.
	pol.engineSpec.overlayDirs = []string{mockConfigDirForOverlay}

	projectDir := t.TempDir()
	requireEmptyProject(t, projectDir)

	contextFile := filepath.Join(projectDir, "MOCK_CONTEXT.md")
	projectSkill := filepath.Join(projectDir, mockConfigDirForOverlay, "skills", "reviewer", "SKILL.md")

	var overlayRoot string
	deliverInContainer(t, pol, projectDir, func(ws Workspace) {
		// Control: the SAME turn delivered a non-overlaid surface through the
		// mount. Everything below is about where the overlaid one went, which
		// only means something once this is true.
		requireEventuallyFile(t, contextFile, "the mock context surface (not overlaid)")
		assert.Contains(t, readHostFile(t, contextFile), runFragmentBody,
			"the non-overlaid surface must still reach the mounted project")

		// The overlay's host side. Index 0 because .mock is overlayDirs[0];
		// containerConfigOverlay names them cfg0, cfg1, … under the scratch
		// root.
		overlayRoot = filepath.Join(containerScratchRoot(t, ws), "cfg0")
		overlaySkill := filepath.Join(overlayRoot, "skills", "reviewer", "SKILL.md")
		requireEventuallyFile(t, overlaySkill, "the skill package inside the scratch overlay")
		assert.Contains(t, readHostFile(t, overlaySkill), runSkillBody,
			"the overlaid delivery must be findable at the overlay's host side — otherwise this test proves the container wrote nowhere, not that the overlay swallowed it")

		// The claim itself: the host PROJECT never sees it.
		_, err := os.Stat(projectSkill)
		assert.True(t, os.IsNotExist(err),
			"the overlaid managed-config delivery must NOT land in the bind-mounted host project (stat err %v) — that is what containerConfigOverlay's scratch mount is for", err)
	})
	require.NotEmpty(t, overlayRoot, "the observation never ran; every assertion above would be vacuous")

	// Teardown discards the overlay wholesale: the scratch root goes, taking
	// the delivered skill with it, and nothing is copied back into the project.
	// This is the residue the feature file states — stated here as an
	// executable claim rather than prose.
	_, err := os.Stat(overlayRoot)
	assert.True(t, os.IsNotExist(err),
		"the container run's scratch overlay must be removed at teardown (stat err %v)", err)
	assertGone(t, projectSkill, "the overlaid skill package")
	assertGone(t, contextFile, "the mock context file")
}

// mockConfigDirForOverlay is the mock engine's managed-config directory — the
// parent of its skills tree (backends.mockSkillsDirName is ".mock/skills"),
// which is what a spec overlays: containerConfigOverlay takes DIRECTORIES
// the engine's writers target under the run's cwd.
const mockConfigDirForOverlay = ".mock"

// containerRunRuntime gates on a reachable container runtime, prints what was
// probed, and returns the ISOLATION runtime for it — the product's own
// Docker/Podman seam, selected by the name the cell probed, so the skip
// vocabulary and the thing under test cannot disagree about which runtime this
// host has. It also builds the hermetic image the policy will launch.
func containerRunRuntime(t *testing.T, what string) Runtime {
	t.Helper()
	ctx := t.Context()

	probe, decision, msg := containercell.Select(ctx, what)
	t.Log("\n" + containercell.Report(containercell.Detect(ctx)))
	dockergate.Apply(t, decision, msg)

	// The image the POLICY launches must carry ctxloom where the policy execs
	// it. The cell states that path as RunPolicyBinary; this package states it
	// as defaultContainerBinary. A drift between them produces a container that
	// starts and dies with no such file — which from the host looks exactly
	// like a plugin that came up and answered nothing.
	require.Equal(t, defaultContainerBinary, containercell.RunPolicyBinary,
		"the cell's run-policy binary path must be the one this package execs in-container")
	require.NoError(t, probe.EnsureRunPolicyImage(ctx),
		"the hermetic run-policy image must build (FROM scratch + one static ctxloom + the probe cat)")

	rt := SelectRuntime(probe.Command)
	require.Equal(t, probe.Command, rt.Name(),
		"the isolation runtime must be the one the cell probed (%s), not a fallback", probe.Command)
	return rt
}

// containerRunPolicy builds the Container policy under test: the claude-code
// spec (whose auth resolver an env var can satisfy — see WithImage's doc for
// why a harness image rides a real spec) over the cell's hermetic image.
//
// The API key is a placeholder and is never used: the plugin that runs inside
// is `mock`, which calls no provider. It exists because PrepareWorkspace's gate
// refuses to launch a container it cannot authenticate — a container run with
// no resolvable auth degrades rather than starting, which would leave this test
// asserting against a HOST delivery and prove nothing about a container.
func containerRunPolicy(t *testing.T, rt Runtime) Container {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "container-delivery-itest-not-a-real-key")
	return NewContainerFor(rt, "claude-code").WithImage(containercell.RunPolicyImageTag)
}

// deliverInContainer runs ONE real containerized turn against projectDir and
// calls observe while it is live. The mock's interactive echo mode blocks the
// turn on stdin, so the window stays open until this function sends "quit";
// after that the turn ends, the shared Cleanup runs, and the caller may assert
// on the post-teardown state. It returns the workspace the policy prepared.
func deliverInContainer(t *testing.T, pol Container, projectDir string, observe func(ws Workspace)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), runTurnDeadline)
	defer cancel()

	ws, err := pol.PrepareWorkspace(ctx, projectDir, runAgentID)
	require.NoError(t, err, "PrepareWorkspace must build the container workspace (runtime + image + auth gate)")
	require.Equal(t, projectDir, ws.Dir(), "the host base's cwd is the identical-path project dir")

	client, err := pol.SpawnClient("mock", "", 0, ws, nil)
	require.NoError(t, err, "SpawnClient must bring the plugin up INSIDE the container and connect")
	killed := false
	defer func() {
		if !killed {
			client.Kill()
			_ = ws.Cleanup()
		}
	}()

	stdinR, stdinW := io.Pipe()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = client.Run(ctx, containerTurnRequest(ws.Dir()), stdinR, io.Discard, io.Discard, nil)
	}()

	observe(ws)

	// The "quit" SENTINEL, not an EOF, is what ends the turn: the client's
	// stdin pump (grpc.GRPCClient.RunWithModelInfo) returns on a read error
	// WITHOUT half-closing the send direction, so a closed stdin never reaches
	// the plugin and the mock's echo loop would block until this test's
	// deadline. The mock documents the sentinel for exactly this reason (a tty
	// rarely EOFs either).
	_, _ = io.WriteString(stdinW, "quit\n")
	_ = stdinW.Close()
	select {
	case <-runDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the containerized turn did not finish after its stdin closed")
	}
	client.Kill()
	require.NoError(t, ws.Cleanup(), "the container workspace must tear down cleanly")
	killed = true
}

// containerTurnRequest is the turn the container runs: one fragment (the
// context surface's payload) and one skill package with a declared-executable
// script (the skills surface's payload), in the mock's stdin-blocking echo mode
// so the turn holds still while the host observes it.
func containerTurnRequest(workDir string) *pb.RunStart {
	return &pb.RunStart{
		Fragments: []*pb.Fragment{{Content: runFragmentBody}},
		Options: &pb.RunOptions{
			WorkDir: workDir,
			Mode:    pb.ExecutionMode_INTERACTIVE,
			// A container IS the process-isolated cell; sending anything else
			// would exercise a delivery shape this run is not in.
			CellKind: pb.CellKindToProto(agent.CellKindProcessIsolated),
			Env:      map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		},
		// The host ships a managed config on every non-skip-setup run; the
		// cells seam short-circuits on a nil one and would deliver nothing.
		ManagedConfig: pb.ManagedConfigToProto(&agent.ManagedConfig{
			Hooks: &wire.HooksConfig{},
			Skills: []agent.SkillExport{{
				Name:    "reviewer",
				Enabled: true,
				Files: []agent.PackageFile{
					{RelPath: "SKILL.md", Content: []byte("---\nname: reviewer\ndescription: container run delivery fixture\n---\n" + runSkillBody + "\n"), Mode: 0o644},
					// 0755 DECLARED (not read off a fixture's filesystem) is
					// what makes the delivered exec bit a claim about the
					// declaration travelling across the boundary.
					{RelPath: "scripts/run.sh", Content: []byte(runScriptBody), Mode: 0o755},
				},
			}},
		}),
	}
}

// containerScratchRoot reads the run's host-side scratch root off the prepared
// workspace — the tree containerConfigOverlay backs its overlay mounts with,
// and the only host-side vantage point on what an overlaid in-container write
// actually produced.
func containerScratchRoot(t *testing.T, ws Workspace) string {
	t.Helper()
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok, "expected a container workspace, got %T", ws)
	require.NotEmpty(t, cw.scratchRoot, "the container workspace must carry its scratch root")
	return cw.scratchRoot
}

// requireEmptyProject refuses to run against a project dir that already holds
// anything: every assertion here is "the container put this here", and a
// leftover would let all of them pass on bytes no container ever wrote.
func requireEmptyProject(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "the project dir must be empty before the container runs, or these rows could pass on a host-side leftover")
}

// requireEventuallyFile waits for a non-empty file to appear. Non-empty
// deliberately: an atomic write+rename is visible only when complete, but a
// zero-byte file is this codebase's characteristic bug and must never satisfy
// a wait.
func requireEventuallyFile(t *testing.T, path, what string) {
	t.Helper()
	require.Eventually(t, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Size() > 0
	}, 90*time.Second, 25*time.Millisecond,
		"%s never appeared at %q while the container's turn was live (Setup delivers before Execute; Cleanup strips it right after)", what, path)
}

func readHostFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a path the test itself composed
	require.NoError(t, err)
	return string(b)
}

func assertHostMode(t *testing.T, path string, want os.FileMode, what string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm(),
		"%s at %q has mode %04o, want %04o — the POSIX mode did not survive the container boundary", what, path, info.Mode().Perm(), want)
}

func assertGone(t *testing.T, path, what string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "%s must be gone after the turn's teardown, stat err %v", what, err)
}
