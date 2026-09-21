package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// refusedFixture is a runState at the point the resolver has just failed:
// the startup gate open over w, nothing else built.
func refusedFixture(w *bytes.Buffer, mode strictness.Mode) *runState {
	return &runState{cfg: config.NewFixture(config.Fixture{}), gates: newPhaseGates(w, mode)}
}

// cellRefusal is the cell adapter's refusal as the resolver returns it: the
// typed sentinel wrapping the gate's own rendering.
func cellRefusal(msg string) error {
	return fmt.Errorf("%w: isolation: refusing to run this member: %s", launch.ErrRuntimeUnavailable, msg)
}

// TestRunState_Refused_CellRefusalIsTheFatalAbort: the cell's refusal
// (launch.ErrRuntimeUnavailable) is a finding the resolver RECORDED on its
// way out, so it is the fatal-findings abort — the class-tagged listing and
// its exit code — never the plain error whose message merely repeats the
// finding. The cell is prepared inside the resolver, so its refusal returns
// as an error before the startup gate ever closes; without this the finding
// is recorded, checked by nothing, and the run exits 1 — the failure the
// gate exists to prevent.
func TestRunState_Refused_CellRefusalIsTheFatalAbort(t *testing.T) {
	resetStrictness(t)
	var out bytes.Buffer
	st := refusedFixture(&out, strictness.Mode{})
	strictness.FailAlways(strictness.ClassIsolation, "start the container runtime", "container-rootless requested but no container runtime is available")

	err := st.refused(cellRefusal("container-rootless requested but no container runtime is available"))

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr, "a recorded finding aborts through the gate, not as the resolver's own error")
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, out.String(), "["+string(strictness.ClassIsolation)+"]")
	assert.Contains(t, out.String(), "no container runtime is available")
	assert.Contains(t, out.String(), "--degraded does NOT bypass", "a non-degradable finding says so in the header")
}

// TestRunState_Refused_DegradedStillAbortsOnNonDegradable: --degraded does
// not turn a NonDegradable cell refusal into a plain error either; the
// abort stays the gate's.
func TestRunState_Refused_DegradedStillAbortsOnNonDegradable(t *testing.T) {
	resetStrictness(t)
	var out bytes.Buffer
	st := refusedFixture(&out, strictness.Mode{Degraded: true})
	strictness.FailAlways(strictness.ClassIsolation, "fix", "requested container could not start")

	err := st.refused(cellRefusal("requested container could not start"))

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
}

// TestRunState_Refused_EmptyAssemblyWithAFindingAbortsThroughTheGate: a
// profile set that assembled to nothing BECAUSE a fatal finding was recorded
// on the way (a bundle that did not load, a parent that did not resolve) is
// that finding's refusal: the gate reports it, with the fix, rather than the
// resolver's symptom-only "assembled to nothing". Nothing pads a profile set
// — companion content rides beside the selection, not inside it — so the
// empty assembly is exactly what the finding predicts.
func TestRunState_Refused_EmptyAssemblyWithAFindingAbortsThroughTheGate(t *testing.T) {
	resetStrictness(t)
	var out bytes.Buffer
	st := refusedFixture(&out, strictness.Mode{})
	strictness.Fail(strictness.ClassBundle, "fix the profile's bundle list", "failed to load bundle \"does-not-exist\"")
	cause := fmt.Errorf("%w: profile set [broken]", launch.ErrContextEmpty)

	err := st.refused(cause)

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr, "the recorded finding is why the set is empty; it aborts through the gate")
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, out.String(), "does-not-exist")
}

// TestRunState_Refused_OtherRefusalsReturnAsTheyCame: every other resolver
// refusal is its own message, findings or not. An empty assembly with NO
// finding recorded is the resolver's own error (nothing else explains it);
// an explicit -f selection that resolved to nothing refuses as the
// empty-selection error — and that error is what the caller reads
// (TestRunCharacterization_ExplicitFragmentMissFailsLoudly), not a gate
// listing.
func TestRunState_Refused_OtherRefusalsReturnAsTheyCame(t *testing.T) {
	resetStrictness(t)
	var out bytes.Buffer
	st := refusedFixture(&out, strictness.Mode{})

	empty := fmt.Errorf("%w: profile set [quiet]", launch.ErrContextEmpty)
	require.Same(t, empty, st.refused(empty), "no finding recorded: the resolver's own error is the abort")
	assert.Empty(t, out.String(), "the gate did not report")

	strictness.Fail(strictness.ClassRef, "fix the ref", "failed to load fragment \"no-such-fragment\"")
	miss := errors.New("no fragments loaded: requested fragments not found: no-such-fragment")
	require.Same(t, miss, st.refused(miss), "an explicit selection miss is its own error, findings or not")
	assert.Empty(t, out.String(), "the gate did not report; the resolver's own error is the abort")

	plain := errors.New("launch: no agent \"nobody\"")
	require.Same(t, plain, st.refused(plain))
}
