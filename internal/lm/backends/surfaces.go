package backends

import (
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// This file is the name→Declaration seam: the single place a caller that
// holds only a backend NAME (operations.MaterializeProfile) reaches that
// backend's declared approaches, without importing the concrete backend. It
// mirrors GetSettingsWriter (name→settings writer) over the descriptor table,
// so surface delivery is provider-correct by construction — every approach is
// the engine's own — and adding a backend means registering ONE descriptor,
// not touching a cross-backend switch here.

// Declared returns the named backend's static Declaration — per surface
// kind, the approaches it constructs and its default — or an empty one for a
// backend that materializes no surfaces or an unregistered name, so a caller
// can Select over it unconditionally.
//
// It is PURE: nothing is constructed and no root is resolved. Help text and
// shell completion describe an engine through it before the user has chosen
// a profile; building real inputs to answer them would mean loading bundles
// to print a table, and — the line the open-sets ruling draws — enumerating
// an engine's surfaces must never need a primed instance.
func Declared(name string) agent.Declaration {
	if h, ok := engines.Hosted(name); ok {
		return h.Declaration()
	}
	return agent.Declaration{}
}

// SurfacesFor is Declared for a caller that must distinguish "unknown
// engine" from "an engine with no surfaces": the former is an error, the
// latter an empty Declaration that renders as "no surface information" rather
// than as an engine with no surfaces.
func SurfacesFor(engine string) (agent.Declaration, error) {
	if _, ok := engines.Hosted(engine); !ok {
		return nil, fmt.Errorf("unknown engine %q", engine)
	}
	return Declared(engine), nil
}

// KnownApproachNames is the union of every approach name any registered
// backend declares, sorted — what a CLI offers as "names that exist at all"
// before an engine is chosen. Derived from the declarations, never listed.
func KnownApproachNames() []string {
	var decls []agent.Declaration
	for _, n := range engines.Registry().Names(nil) {
		decls = append(decls, Declared(string(n)))
	}
	return agent.ApproachNames(decls...)
}

// UncarriedSurfaces is the delivery's inverse over the SAME inputs: the parts of
// a run's assembled loadout the named engine has NO structural place for. A
// delivery report can only list what it wrote — every line of it true — so the
// loss is invisible in it by construction; this is where a caller gets the other
// half.
//
// It reports a loss ONLY when the inputs actually carry the thing that cannot be
// delivered. A capability gap nobody asked to use costs nothing and stays quiet.
// Hooks are the only loss: an engine that has a hook mechanism but declares
// unified events it has no native form for (engine.Definition.HookLosses). A
// surface KIND an engine declares no approach for is not one: every such
// absence is a FOLD, and reporting a folded surface as lost would be a false
// alarm — the fastest way to get the real line ignored.
func UncarriedSurfaces(name string, in agent.SurfaceInputs) []agent.SurfaceLoss {
	e, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok || in.Hooks == nil {
		return nil
	}
	return unsupportedHookKindLosses(e.Root().HookLosses, *in.Hooks)
}

// unsupportedHookKindLosses reports, for an engine that carries hooks
// generally but declares specific unified EVENTS it has no native form for
// (engine.Definition.HookLosses), the ones the inputs actually configure —
// the "only when it costs something" rule. Sorted by event so the report is
// stable across a map's randomized range order.
func unsupportedHookKindLosses(losses map[string]string, hooks wire.HooksConfig) []agent.SurfaceLoss {
	if len(losses) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(losses))
	for k := range losses {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var out []agent.SurfaceLoss
	for _, kind := range kinds {
		n := len(hooks.Unified.Event(kind))
		if n == 0 {
			continue
		}
		out = append(out, agent.SurfaceLoss{Surface: "hooks", Detail: fmt.Sprintf("%d %s", n, kind), Reason: losses[kind]})
	}
	return out
}
