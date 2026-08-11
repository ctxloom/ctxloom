package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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

// The strict startup gate is the whole point of fail-loudly: a collected fatal
// finding must abort `ctxloom run`/`mcp` before launch with the distinct
// findings exit code (3), listing every finding and its fix-it plus the
// --degraded escape hatch. In degraded mode the same finding must NOT abort.
func TestFailOnFindings_StrictAbortsWithFindingsExitCode(t *testing.T) {
	t.Run("strict mode aborts, lists finding + fix-it", func(t *testing.T) {
		resetStrictness(t)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassSync,
			"check the remote/network, or pass --degraded to launch anyway",
			"sync failed: %v", "boom")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		require.Error(t, err, "a collected fatal finding must abort startup in strict mode")
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr, "the abort must be an ExitError so deferred cleanup runs")
		assert.Equal(t, exitCodeFatalFindings, exitErr.Code,
			"strict abort must carry the distinct findings exit code (3), not the generic 1")

		out := buf.String()
		assert.Contains(t, out, "aborting startup", "header must announce the abort")
		assert.Contains(t, out, "[sync]", "each finding is class-tagged for diagnosis")
		assert.Contains(t, out, "sync failed: boom", "the finding message must be listed")
		assert.Contains(t, out, "check the remote/network", "the fix-it must be listed")
		assert.Contains(t, out, "--degraded", "the escape hatch must be surfaced")
	})

	t.Run("degraded mode never aborts", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassSync,
			"check the remote/network, or pass --degraded to launch anyway",
			"sync failed: %v", "boom")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		assert.NoError(t, err, "degraded mode is the escape hatch — it must launch anyway")
		assert.Empty(t, buf.String(), "degraded mode renders no findings block")
	})
}

// An explicitly-requested-but-unsatisfiable container runtime is a fail-loudly
// finding (ClassIsolation) raised inside isolation.Prepare. The run path
// re-gates on it right after Prepare (isolation resolves AFTER the main startup
// gate), so it must abort with the distinct findings exit code (3) — before an
// UNSANDBOXED engine spawns — and stay a no-op under --degraded (the host
// degrade). This pins the class→choke mapping the run path relies on.
func TestFailOnFindings_ContainerIsolationDegradeIsFatalUnlessDegraded(t *testing.T) {
	const fixit = "install/build the agent image and start the container runtime (docker/podman), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox"

	t.Run("strict mode aborts exit 3, names the lost boundary + escape hatch", func(t *testing.T) {
		resetStrictness(t)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassIsolation, fixit,
			"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", "agent-a", "image absent")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		require.Error(t, err, "an explicitly-requested unsatisfiable container must abort before an unsandboxed launch")
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, exitCodeFatalFindings, exitErr.Code, "must carry the distinct findings exit code (3)")

		out := buf.String()
		assert.Contains(t, out, "[isolation]", "the finding must be class-tagged for diagnosis")
		assert.Contains(t, out, "NOT sandboxed", "the abort must name the lost container boundary")
		assert.Contains(t, out, "--degraded", "the escape hatch must be surfaced")
	})

	t.Run("degraded mode never aborts — the host degrade is the accepted outcome", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassIsolation, fixit,
			"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", "agent-a", "image absent")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		assert.NoError(t, err, "--degraded downgrades the isolation finding to the plain host degrade")
		assert.Empty(t, buf.String(), "degraded mode renders no findings block")
	})
}

// The run path has TWO findings gates: gate 1 after config/sync/assembly
// (anchored at startupMark) and gate 2 after isolation.Prepare. Gate 2 must be
// anchored at a checkpoint captured IMMEDIATELY after gate 1 passes so the two
// windows tile: a finding recorded in the gap between them (trust gate, session
// accounting, hook work on the way to the launch) is still fatal. This pins the
// tiling contract run.go wires (postStartupMark); the old anchoring — a
// checkpoint taken just before Prepare — left the gap ungated.
func TestStartupGates_TileWithoutHole(t *testing.T) {
	resetStrictness(t)

	// Gate 1 passes clean.
	startupMark := strictness.Checkpoint()
	var gate1 bytes.Buffer
	require.NoError(t, failOnFindings(&gate1, startupMark), "gate 1 passes with nothing recorded")

	// run.go's anchoring: the gate-2 mark is captured immediately after gate 1.
	postStartupMark := strictness.Checkpoint()

	// A fault fires IN THE GAP — after gate 1, before isolation.Prepare.
	strictness.Fail(strictness.ClassTrust, "remove or restore the trust store", "trust store unreadable: boom")

	// The OLD anchoring (a checkpoint taken only when Prepare runs) misses it…
	lateMark := strictness.Checkpoint()
	var lateGate bytes.Buffer
	assert.NoError(t, failOnFindings(&lateGate, lateMark),
		"a gate anchored AFTER the gap cannot see the gap finding — the hole this test pins closed")

	// …the tiled anchoring catches it.
	var gate2 bytes.Buffer
	err := failOnFindings(&gate2, postStartupMark)
	require.Error(t, err, "gate 2 anchored at the post-gate-1 checkpoint must catch a gap finding")
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, gate2.String(), "trust store unreadable")
}

func TestLoadConfigOrFallback_Success(t *testing.T) {
	want := config.NewFixture(config.Fixture{AppPaths: []string{"/some/dir"}})
	var buf bytes.Buffer

	got := loadConfigOrFallback(func() (*config.Config, error) {
		return want, nil
	}, &buf)

	assert.Same(t, want, got, "should return loader's config unchanged on success")
	assert.Empty(t, buf.String(), "no warning on success")
}

func TestLoadConfigOrFallback_FailureReturnsMinimalDefault(t *testing.T) {
	var buf bytes.Buffer

	got := loadConfigOrFallback(func() (*config.Config, error) {
		return nil, errors.New("config: missing required field")
	}, &buf)

	assert.NotNil(t, got)
	assert.Equal(t, []string{".ctxloom"}, got.GetAppPaths(),
		"fallback config must be rooted at .ctxloom so downstream operations have somewhere to look")

	out := buf.String()
	assert.Contains(t, out, "ctxloom: warning:",
		"warning must use the project-standard ctxloom: warning: prefix")
	assert.Contains(t, out, "failed to load config",
		"warning must explain what failed")
	assert.Contains(t, out, "config: missing required field",
		"warning must include the underlying error for diagnosis")
	assert.Contains(t, out, ".ctxloom",
		"warning must tell the user where the fallback is rooted")
}
