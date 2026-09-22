package operations

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines"
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
	decl, serr := engineDeclaration(engine)
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

// ResolveAgentRoots validates a binding's root selection (kind -> root
// label, as written) against the named engine's declared approaches: the
// kind must be one the engine carries and the root one its approach for
// that kind OFFERS. It returns the selection as delivery.Route reads it.
func ResolveAgentRoots(engineName string, declared map[string]string) (map[present.Kind]present.RootKind, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	kind, ok := engines.Registry().Lookup(engine.Name(engineName))
	if !ok {
		return nil, fmt.Errorf("roots: no engine kind %q is composed", engineName)
	}
	surfaces := kind.Root().Surfaces()
	out := make(map[present.Kind]present.RootKind, len(declared))
	for name, label := range declared {
		k, ok := present.ParseKind(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("roots %s=%s: %q is not a surface kind", name, label, name)
		}
		root, ok := present.ParseRootKind(strings.TrimSpace(label))
		if !ok {
			return nil, fmt.Errorf("roots %s=%s: %q is not a root (session-home, project-root, work-dir)", name, label, label)
		}
		a, carried := surfaces[k]
		if !carried {
			return nil, fmt.Errorf("roots %s=%s: %s declares no %s surface", name, label, engineName, k)
		}
		if !a.Traits().Offers(root) {
			return nil, fmt.Errorf("roots %s=%s: %s's %s approach (%s) offers %v, not %s", name, label, engineName, k, a.Name(), a.Traits().Roots, root)
		}
		out[k] = root
	}
	return out, nil
}

// engineDeclaration is the named engine's named-form table (agent.Hosted),
// distinguishing "unknown engine" (an error) from "an engine with no
// surfaces" (an empty Declaration that renders as "no surface information").
func engineDeclaration(name string) (agent.Declaration, error) {
	h, ok := engines.Hosted(name)
	if !ok {
		return nil, fmt.Errorf("unknown engine %q", name)
	}
	return h.Declaration(), nil
}
