package operations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
// system-prompt is claude-only; a kiro agent naming it has made a mistake worth
// hearing about, and silently giving it a native-file delivery instead would
// teach it the request had worked.
func ResolveAgentSurfaces(engine string, declared map[string]string) (map[agent.SurfaceKind]agent.Approach, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	set, serr := backends.SurfacesFor(engine)
	if serr != nil {
		return nil, fmt.Errorf("surfaces: %w", serr)
	}
	out := make(map[agent.SurfaceKind]agent.Approach, len(declared))
	for name, approach := range declared {
		kind, err := agent.ParseSurfaceKind(strings.TrimSpace(name))
		if err != nil {
			return nil, fmt.Errorf("surfaces: %w", err)
		}
		a, aerr := agent.ParseApproach(strings.TrimSpace(approach))
		if aerr != nil {
			return nil, fmt.Errorf("surfaces %s: %w", name, aerr)
		}
		supported := set.SupportedApproaches(kind)
		if !containsApproach(supported, a) {
			return nil, fmt.Errorf("surfaces %s=%s: %s does not support it (supports: %s)",
				name, approach, engine, approachList(supported))
		}
		out[kind] = a
	}
	return out, nil
}

func containsApproach(in []agent.Approach, want agent.Approach) bool {
	for _, a := range in {
		if a == want {
			return true
		}
	}
	return false
}

// approachList renders an engine's supported approaches for an error a human
// reads. Sorted by NAME so the message is stable — the declaration order is
// meaningful to the code (first is the at-rest default) and meaningless here.
func approachList(in []agent.Approach) string {
	if len(in) == 0 {
		return "none — this engine folds the surface into another"
	}
	names := make([]string, 0, len(in))
	for _, a := range in {
		names = append(names, a.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
