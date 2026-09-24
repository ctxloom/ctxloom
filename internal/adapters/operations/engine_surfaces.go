package operations

import (
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// This file is the operations' read of an engine's DECLARED surface facts by
// name: the approach names a binding may select (agent.Hosted.Declaration)
// and the hook events the engine declares it cannot carry
// (engine.Definition.HookLosses).

// KnownApproachNames is the union of every approach name any composed
// engine declares, sorted — what a CLI offers as "names that exist at all"
// before an engine is chosen. Derived from the declarations, never listed.
func KnownApproachNames(reg engine.Registry) []string {
	var decls []agent.Declaration
	for _, n := range reg.NamesWhere(func(_ engine.Name, e engine.Engine) bool { _, ok := e.(agent.Hosted); return ok }) {
		h, _ := agent.HostedIn(reg, string(n))
		decls = append(decls, h.Declaration())
	}
	return agent.ApproachNames(decls...)
}

// uncarriedSurfaces is the delivery's inverse over the SAME inputs: the parts of
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
func uncarriedSurfaces(reg engine.Registry, name string, in agent.SurfaceInputs) []agent.SurfaceLoss {
	e, ok := reg.Lookup(engine.Name(name))
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
