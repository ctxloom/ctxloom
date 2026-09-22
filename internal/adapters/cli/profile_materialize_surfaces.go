package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// This file carries `agent edit --surface <kind>=<approach>`'s parser: the
// binding's named-form preference the host plugin arm still reads.
//
// The vocabulary is ctxloom's own, which is the problem it has to solve. Every
// name a user types here is derived from the enums in internal/core/agent —
// never restated — so the flag and its error text cannot drift from each
// other or from the code.

// parseSurfaceOverrides turns repeated `kind=approach` pairs into the map
// MaterializeProfileRequest carries.
//
// Both halves are validated HERE rather than deep in delivery, so a typo is
// reported against the flag the user typed instead of surfacing later as a
// surface that quietly kept its default. Whether the pair is SUPPORTED by the
// chosen engine is a different question, answered by the builder's Build()
// against that engine's own Declaration — this only rejects names that exist
// nowhere, i.e. that NO registered engine declares.
func parseSurfaceOverrides(pairs []string) (map[agent.SurfaceKind]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	known := operations.KnownApproachNames()
	out := make(map[agent.SurfaceKind]string, len(pairs))
	for _, p := range pairs {
		name, approach, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--surface %q is not <kind>=<approach> (e.g. --surface context=unsafe-file; kinds: %s)",
				p, strings.Join(agent.SurfaceKindNames(), ", "))
		}
		k, err := agent.ParseSurfaceKind(strings.TrimSpace(name))
		if err != nil {
			return nil, fmt.Errorf("--surface %q: %w", p, err)
		}
		a := strings.TrimSpace(approach)
		if !slices.Contains(known, a) {
			return nil, fmt.Errorf("--surface %q: unknown approach %q (known: %s)", p, a, strings.Join(known, ", "))
		}
		if prev, dup := out[k]; dup && prev != a {
			return nil, fmt.Errorf("--surface names %s twice, as %s and %s; a surface is delivered one way",
				k, prev, a)
		}
		out[k] = a
	}
	return out, nil
}
