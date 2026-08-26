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

		var buf bytes.Buffer
		gates := newPhaseGates(&buf)
		strictness.Fail(strictness.ClassSync,
			"check the remote/network, or pass --degraded to launch anyway",
			"sync failed: %v", "boom")

		err := gates.close(PhaseStartup)

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

		var buf bytes.Buffer
		gates := newPhaseGates(&buf)
		strictness.Fail(strictness.ClassSync,
			"check the remote/network, or pass --degraded to launch anyway",
			"sync failed: %v", "boom")

		err := gates.close(PhaseStartup)

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

		var buf bytes.Buffer
		gates := newPhaseGates(&buf)
		strictness.Fail(strictness.ClassIsolation, fixit,
			"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", "agent-a", "image absent")

		err := gates.close(PhaseWorkspace)

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

		var buf bytes.Buffer
		gates := newPhaseGates(&buf)
		strictness.Fail(strictness.ClassIsolation, fixit,
			"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", "agent-a", "image absent")

		err := gates.close(PhaseWorkspace)

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

	var out bytes.Buffer
	gates := newPhaseGates(&out)

	// Gate 1 passes clean.
	require.NoError(t, gates.close(PhaseStartup), "gate 1 passes with nothing recorded")

	// A fault fires IN THE GAP — after gate 1, before the workspace phase does
	// anything. Under the old hand-anchored scheme this was the hole: a gate
	// anchored when the next phase STARTED could not see it, and the tiling
	// that closed the hole was a convention two comments described.
	strictness.Fail(strictness.ClassTrust, "remove or restore the trust store", "trust store unreadable: boom")

	// The contrast, kept because it is what makes the property non-obvious: a
	// mark taken AFTER the gap genuinely cannot see the gap finding.
	lateMark := strictness.Checkpoint()
	assert.Empty(t, strictness.Since(lateMark),
		"a window opened after the gap cannot see the gap finding — the hole this test pins closed")

	// close() re-opened as it closed, so gate 2's window began the instant gate
	// 1 ended and the gap finding is inside it. No caller anchored anything.
	err := gates.close(PhaseWorkspace)
	require.Error(t, err, "the window gate 1 opened as it closed must catch a gap finding")
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, out.String(), "trust store unreadable")
	assert.Contains(t, out.String(), "aborting workspace",
		"the header must name the phase that refused, not the phase before it")
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

// TestPhaseGates_NonDegradableSurvivesDegraded pins the property the whole
// per-finding mechanism exists for, at the gate rather than in strictness: a
// finding whose harm IS the launch must abort under --degraded, while an
// ordinary finding in the same window must not.
//
// Both arms are asserted because either alone is vacuous. "The non-degradable
// one aborts" passes if degraded were ignored entirely; "the ordinary one does
// not" passes if nothing ever aborted.
func TestPhaseGates_NonDegradableSurvivesDegraded(t *testing.T) {
	t.Run("degraded still aborts on a non-degradable finding", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		var buf bytes.Buffer
		gates := newPhaseGates(&buf)
		strictness.Fail(strictness.ClassSync, "ordinary remedy", "an ordinary degradable fault")
		strictness.FailAlways(strictness.ClassIsolation, "start the runtime it promised",
			"container %q was started but never reached running state", "agent-a")

		err := gates.close(PhaseTransportStart)

		require.Error(t, err, "--degraded must not swallow a finding whose harm is the launch itself")
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, exitCodeFatalFindings, exitErr.Code)

		out := buf.String()
		assert.Contains(t, out, "never reached running state", "the abort must name the fault")
		assert.Contains(t, out, "aborting transport start", "the header must name the phase that refused")
		assert.NotContains(t, out, "an ordinary degradable fault",
			"the degradable finding must not be dragged into the abort alongside it")
	})

	t.Run("degraded alone still never aborts", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		var buf bytes.Buffer
		gates := newPhaseGates(&buf)
		strictness.Fail(strictness.ClassSync, "ordinary remedy", "an ordinary degradable fault")

		assert.NoError(t, gates.close(PhaseTransportStart), "an ordinary finding still degrades")
		assert.Empty(t, buf.String())
	})
}

// TestPhaseGates_WindowsAreDisjoint pins the half of tiling that the
// contiguity test cannot see, and that a mutation proved was unasserted.
//
// Tiling is TWO properties: windows must abut (no finding falls in a gap) and
// they must be DISJOINT (a finding belongs to exactly one phase). A close()
// that reports but never re-opens still satisfies contiguity — its window
// simply never advances, so it accumulates and the gap finding is caught for
// the wrong reason. What it breaks is disjointness: every later gate
// re-reports every earlier finding, so one broken config aborts three phases
// and the operator is told the same thing three times with three different
// phase names.
func TestPhaseGates_WindowsAreDisjoint(t *testing.T) {
	resetStrictness(t)

	var out bytes.Buffer
	gates := newPhaseGates(&out)

	strictness.Fail(strictness.ClassConfig, "fix the config", "fault ALPHA")
	require.Error(t, gates.close(PhaseStartup), "phase 1 aborts on its own finding")
	require.Contains(t, out.String(), "fault ALPHA")

	out.Reset()
	strictness.Fail(strictness.ClassSync, "fix the sync", "fault BRAVO")
	require.Error(t, gates.close(PhaseWorkspace), "phase 2 aborts on its own finding")

	second := out.String()
	assert.Contains(t, second, "fault BRAVO", "phase 2 must report what phase 2 collected")
	assert.NotContains(t, second, "fault ALPHA",
		"phase 1's finding must NOT reappear: close() re-opens, so each finding belongs to exactly one phase")
}
