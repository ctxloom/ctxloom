package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// An MCP client is told that conditional guidance EXISTS and where to ask for
// it. The catalog is pulled rather than pushed, so the pointer is the only part
// a client cannot derive on its own: an agent that never learns the catalog is
// there never asks, and every fragment it would have selected stays invisible.
//
// Asserted against PremiseSelectionInstruction rather than a phrase
// lifted out of it. A copied literal goes vacuous the moment the wording is
// changed — passing forever while checking nothing — and this wording is
// expected to be revised, since its properties were fixed by a measurement
// apparatus that no longer exists.
func TestSessionInstructions_CarryThePremiseSelectionInstruction(t *testing.T) {
	testsupport.Isolate(t)

	for _, harp := range []string{"", "some-session-harp"} {
		got := SessionInstructions(harp)

		assert.Contains(t, got, PremiseSelectionInstruction(),
			"the measured selection wording must reach the client verbatim (harp %q): a re-worded copy drifts and the measured one loses", harp)
		assert.Contains(t, got, FragmentsResourceURI,
			"and it must name the resource the catalog actually lives at (harp %q), or the instruction is unactionable", harp)
	}
}
