package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// This file RENDERS the roster-wide read of capability loss
// (operations.CapabilityLossByAgent) for `ctxloom manage check`; it computes
// nothing of its own. A second way to compute the same fact is how the
// surfaces drift into disagreeing about what a user's engine can carry, which
// is the failure the report exists to prevent.

// capabilityLossLines is the wiring report's form of
// operations.CapabilityLossLines: each line led by "NOT carried by", the
// words `profile materialize` prints for the same fact.
func capabilityLossLines(entries []operations.AgentSurfaceLoss) []string {
	lines := operations.CapabilityLossLines(entries)
	for i, line := range lines {
		lines[i] = "NOT carried by " + line
	}
	return lines
}

// renderCapabilityLosses writes the loss section of a wiring report, and
// writes NOTHING AT ALL when nothing is lost — matching printSurfaceCurrencies'
// rule and UncarriedSurfaces' own: a bare, unlabelled "Capability loss:"
// heading over an empty list is the fastest way to teach a reader to skip the
// line that matters.
func renderCapabilityLosses(w io.Writer, entries []operations.AgentSurfaceLoss) {
	lines := capabilityLossLines(entries)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "\nCapability loss — configured, but this engine has nowhere to put it:")
	for _, line := range lines {
		fmt.Fprintf(w, "  %s\n", line)
	}
}
