package operations

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// ResolveAgentSurfaces parses an agent binding's declared delivery preference
// and checks it against what `engine` can actually do.
//
// One function, two callers, deliberately: the agent WRITE path calls it so a
// bad pair is refused by the command that set it, and the LAUNCH path calls it
// so what runs is what was validated. Splitting them would let the two drift,
// and the drift would show up as a session quietly delivering context a
// different way than the binding asked for.
//
// An unsupported pair is an ERROR, never a downgrade to the engine's default.
// system-prompt is claude-only; an agent bound to any other engine and naming
// it has made a mistake worth hearing about, and silently giving it that
// engine's own delivery instead would teach it the request had worked.
func ResolveAgentSurfaces(engine string, declared map[string]string) (map[agent.SurfaceKind]string, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	decl, serr := backends.SurfacesFor(engine)
	if serr != nil {
		return nil, fmt.Errorf("surfaces: %w", serr)
	}
	out := make(map[agent.SurfaceKind]string, len(declared))
	for name, approach := range declared {
		kind, err := agent.ParseSurfaceKind(strings.TrimSpace(name))
		if err != nil {
			return nil, fmt.Errorf("surfaces: %w", err)
		}
		approach = strings.TrimSpace(approach)
		supported := decl.Names(kind)
		if !slices.Contains(supported, approach) {
			// Names is already sorted, so the message is stable; an empty set
			// is a FOLD (the engine carries this kind inside another surface).
			supports := strings.Join(supported, ", ")
			if supports == "" {
				supports = "none — this engine folds the surface into another"
			}
			return nil, fmt.Errorf("surfaces %s=%s: %s does not support it (supports: %s)",
				name, approach, engine, supports)
		}
		out[kind] = approach
	}
	return out, nil
}
