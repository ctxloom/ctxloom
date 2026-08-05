package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE EXIT-CODE LADDER (docs/cli-ux-principles.md §7). Categorical, not a
// severity scale.

// The ladder is CATEGORICAL, not a severity scale, and its whole value is that
// each code answers a different question. This pins the values and their
// distinctness: collapsing exitCodeRefused onto 1 would report a decision as a
// fault, and onto 0 would make an unattended sync that refused
// indistinguishable from one with nothing to do.
func TestExitCodeLadder_RefusedIsTwoAndDistinctFromErrorAndFindings(t *testing.T) {
	assert.Equal(t, 2, exitCodeRefused, "the documented contract (docs/cli-ux-principles.md §7) is 2")
	assert.Equal(t, 3, exitCodeFatalFindings)
	assert.NotEqual(t, exitCodeRefused, exitCodeFatalFindings)
	assert.NotEqual(t, 0, exitCodeRefused, "a run that deliberately did none of what was asked is not a success")
	assert.NotEqual(t, 1, exitCodeRefused, "a refusal is not an error; 1 would send a user hunting a fault on their own machine")
}

// A refusal must reach the process exit as 2 AND must not be printed as an
// error on top of the refusal message the command already wrote. Both halves
// come from ExitError: exitCodeFor is what Run() consults before its
// print-and-exit-1 fallback.
func TestRefusedExit_CarriesTwoThroughTheExitPathWithoutBeingPrinted(t *testing.T) {
	code, carried := exitCodeFor(refusedExit())
	require.True(t, carried, "the refusal must carry its own code, or Run() prints it and exits 1")
	assert.Equal(t, exitCodeRefused, code)
}
