//go:build acceptance

package acceptance

import (
	"errors"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// TestGateContainerRow_FailDecisionIsAFailureNotASkip pins the defect that
// made CTXLOOM_REQUIRE_DOCKER=1 a no-op for a @container step: the step read
// only Runtime.Available and returned godog.ErrSkip, so the gate's Fail
// decision — "demands one ... ran NOTHING" — was printed under a SKIPPED
// banner and the run still exited 0 with the scenario filed as passed.
func TestGateContainerRow_FailDecisionIsAFailureNotASkip(t *testing.T) {
	w := &World{}
	err := gateContainerRow(w, "the row", dockergate.Fail, "no runtime, and the floor demands one")
	require.Error(t, err)
	assert.False(t, errors.Is(err, godog.ErrSkip), "a Fail decision must never be reported as a skip")
	assert.ErrorContains(t, err, "the floor demands one")
}

func TestGateContainerRow_SkipDecisionIsErrSkipAndRecordsTheReason(t *testing.T) {
	w := &World{}
	err := gateContainerRow(w, "the row", dockergate.Skip, "docker unavailable")
	require.ErrorIs(t, err, godog.ErrSkip)
	assert.Contains(t, w.docStepMaterialized, "docker unavailable")
}

func TestGateContainerRow_ProceedIsNil(t *testing.T) {
	w := &World{}
	assert.NoError(t, gateContainerRow(w, "the row", dockergate.Proceed, ""))
	assert.Empty(t, w.docStepMaterialized)
}
