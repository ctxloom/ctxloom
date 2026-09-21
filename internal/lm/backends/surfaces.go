package backends

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
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
	if d, ok := lookup(name); ok {
		return d.Surfaces
	}
	return agent.Declaration{}
}

// SurfacesFor is Declared for a caller that must distinguish "unknown
// engine" from "an engine with no surfaces": the former is an error, the
// latter an empty Declaration that renders as "no surface information" rather
// than as an engine with no surfaces.
func SurfacesFor(engine string) (agent.Declaration, error) {
	if _, ok := lookup(engine); !ok {
		return nil, fmt.Errorf("unknown engine %q", engine)
	}
	return Declared(engine), nil
}

// KnownApproachNames is the union of every approach name any registered
// backend declares, sorted — what a CLI offers as "names that exist at all"
// before an engine is chosen. Derived from the declarations, never listed.
func KnownApproachNames() []string {
	decls := make([]agent.Declaration, 0, len(records))
	for _, r := range records {
		decls = append(decls, r.host.Surfaces)
	}
	return agent.ApproachNames(decls...)
}

// UncarriedSurfaces is the delivery's inverse over the SAME inputs: the parts of
// a run's assembled loadout the named backend has NO structural place for. A
// delivery report can only list what it wrote — every line of it true — so the
// loss is invisible in it by construction; this is where a caller gets the other
// half.
//
// It reports a loss ONLY when the inputs actually carry the thing that cannot be
// delivered. A capability gap nobody asked to use costs nothing and stays quiet,
// which is the rule agent.RouteUnifiedHooks already applies one level down.
//
// Hooks are the only genuine loss today, in two shapes: a backend with NO hook
// mechanism at all (noHooksReason — opencode) and a backend that has one but
// lacks a native event for a SPECIFIC unified kind (unsupportedHookKinds —
// codex has no session_end). A surface KIND a backend declares no approach for
// is neither: every such absence is a FOLD (codex's and opencode's MCP both
// ride their settings surface), and reporting a folded surface as lost would be
// a false alarm — the fastest way to get the real line ignored.
func UncarriedSurfaces(name string, in agent.SurfaceInputs) []agent.SurfaceLoss {
	d, ok := lookup(name)
	if !ok || in.Hooks == nil {
		return nil
	}
	if d.NoHooksReason != "" {
		detail := droppedHookDetail(name, *in.Hooks)
		if detail == "" {
			return nil
		}
		return []agent.SurfaceLoss{{Surface: "hooks", Detail: detail, Reason: d.NoHooksReason}}
	}
	return unsupportedHookKindLosses(d, *in.Hooks)
}

// LaunchOnlySurfaces is UncarriedSurfaces' sibling for the STATIC path: the
// parts of a run's assembled loadout the named backend delivers ONLY at launch,
// into a per-session engine home, and which a HARPLESS caller (`ctxloom profile
// materialize`, `ctxloom manage install`, a hooks apply outside a run)
// therefore cannot write anywhere (hosting.Hosting.LaunchOnlySettingsReason —
// the launch-delivered mock double declares it).
//
// THE TWO ARE NOT INTERCHANGEABLE and must not be merged. UncarriedSurfaces
// answers "what can this ENGINE never carry" — a fact about the engine, true
// for every caller, which is why `agent show` may report it about a binding it
// will never materialize. This answers "what can a HARPLESS caller not write" —
// a fact about the CALLER. Folding this into UncarriedSurfaces would make
// `agent show` tell a user their codex agent loses its hooks, when an agent
// declaring `engine_home: session` receives every one of them at launch.
//
// Same "only when it costs something" rule as its sibling: a surface the inputs
// do not carry is reported nowhere. Losses come out in a fixed order (settings,
// MCP, commands, skills) so the report can be diffed.
func LaunchOnlySurfaces(name string, in agent.SurfaceInputs) []agent.SurfaceLoss {
	d, ok := lookup(name)
	if !ok || d.LaunchOnlySettingsReason == "" {
		return nil
	}
	var losses []agent.SurfaceLoss
	add := func(surface, detail string) {
		if detail == "" {
			return
		}
		losses = append(losses, agent.SurfaceLoss{Surface: surface, Detail: detail, Reason: d.LaunchOnlySettingsReason})
	}
	if in.Hooks != nil {
		// droppedHookDetail names the events in the user's own config
		// vocabulary — reused verbatim, because "which hooks did not land" is
		// the same question here as it is for a hookless backend.
		add("hooks", droppedHookDetail(name, *in.Hooks))
	}
	add("mcp", managedMCPDetail(in))
	if n := len(in.Commands); n > 0 {
		add("commands", fmt.Sprintf("%d command file(s)", n))
	}
	if n := len(in.Skills); n > 0 {
		add("skills", fmt.Sprintf("%d skill package(s)", n))
	}
	return losses
}

// managedMCPDetail counts the MCP servers a delivery would have registered:
// every server the resolved bundles ship, ctxloom's own (its companion
// loadout's, whose absence costs the user every ctxloom tool) included. "" when
// there are none at all.
func managedMCPDetail(in agent.SurfaceInputs) string {
	n := len(in.BundleMCP)
	if n == 0 {
		return ""
	}
	if _, own := in.BundleMCP[agent.MCPServerName]; own {
		return fmt.Sprintf("%d MCP server(s), including ctxloom's own", n)
	}
	return fmt.Sprintf("%d MCP server(s), NOT including ctxloom's own", n)
}

// unsupportedHookKindLosses reports, for a backend that carries hooks
// generally but declares specific unified KINDS it has no native event for
// (hosting.Hosting.UnsupportedHookKinds), the ones the inputs actually
// configure — same "only when it costs something" rule as the whole-backend
// case above. Sorted by kind so the report is stable across a map's
// randomized range order.
func unsupportedHookKindLosses(d *hosting.Hosting, hooks wire.HooksConfig) []agent.SurfaceLoss {
	if len(d.UnsupportedHookKinds) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(d.UnsupportedHookKinds))
	for k := range d.UnsupportedHookKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var losses []agent.SurfaceLoss
	for _, kind := range kinds {
		n := len(UnifiedEventHooks(hooks.Unified, kind))
		if n == 0 {
			continue
		}
		losses = append(losses, agent.SurfaceLoss{
			Surface: "hooks",
			Detail:  fmt.Sprintf("%d %s", n, kind),
			Reason:  d.UnsupportedHookKinds[kind],
		})
	}
	return losses
}

// stripUnsupportedHookKinds removes the hook kinds the named backend DECLARES
// it has no native event for, so the delivered file matches the loss report.
//
// A declaration without this is a lie in the user's favour and the worse of the
// two directions: unsupportedHookKindLosses tells the user their session_start
// guardrail did not land while the surface writes it anyway, so the report and
// the filesystem disagree and the report is the one people act on. The delivery
// test says it plainly — "the loss report would be telling users something
// false".
//
// Returns the input unchanged when the backend declares nothing, and never
// mutates the caller's config: the same HooksConfig is handed to every surface
// in a run, so stripping in place would silently narrow the others too.
func stripUnsupportedHookKinds(name string, hooks *wire.HooksConfig) *wire.HooksConfig {
	if hooks == nil {
		return nil
	}
	d, ok := lookup(name)
	if !ok || len(d.UnsupportedHookKinds) == 0 {
		return hooks
	}
	stripped := *hooks
	for kind := range d.UnsupportedHookKinds {
		setUnifiedEventHooks(&stripped.Unified, kind, nil)
	}
	// Nothing left to deliver is reported as NO CONFIG, not as an empty one.
	// A surface handed an empty-but-present config writes its managed block
	// anyway — {"hooks":{"unified":{}}} — which claims ctxloom manages hooks
	// here and found none, when the truth is this engine cannot carry the ones
	// that were asked for. Returning nil takes the surface's own existing
	// no-hooks path, so the declaration, the delivery and the loss report all
	// say the same thing.
	if !carriesAnyHook(stripped) {
		return nil
	}
	return &stripped
}

// carriesAnyHook reports whether a config still has a hook to deliver, across
// both the unified events and the backend-native passthrough map.
func carriesAnyHook(h wire.HooksConfig) bool {
	for _, event := range HookEvents() {
		if len(UnifiedEventHooks(h.Unified, event)) > 0 {
			return true
		}
	}
	for _, hs := range h.Ext {
		if len(hs) > 0 {
			return true
		}
	}
	return false
}

// droppedHookDetail names what a hookless backend loses, in the user's own
// vocabulary: the unified events by their config keys (in HookEvents order,
// so the line is stable run to run) plus any backend-native passthrough hooks
// addressed at THIS engine, which are equally undeliverable. "" when the config
// carries nothing.
func droppedHookDetail(name string, hooks wire.HooksConfig) string {
	var parts []string
	for _, event := range HookEvents() {
		if n := len(UnifiedEventHooks(hooks.Unified, event)); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, event))
		}
	}
	var native []string
	for event, hs := range hooks.Ext[name] {
		if len(hs) > 0 {
			native = append(native, fmt.Sprintf("%d %s", len(hs), event))
		}
	}
	sort.Strings(native) // map range order is random; a report that reshuffles cannot be diffed
	parts = append(parts, native...)
	return strings.Join(parts, ", ")
}
