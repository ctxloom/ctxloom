package backends

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file is the name→SurfaceSet seam: the single place a caller that holds
// only a backend NAME (operations.MaterializeProfile) turns a run's shared inputs
// into that backend's per-provider SurfaceSet, without importing the concrete
// backend. It mirrors GetSettingsWriter (name→settings writer) over the descriptor
// table, so surface delivery is provider-correct by construction — routed through
// each backend's own NewSurfaces — and adding a backend means registering ONE
// descriptor, not touching a cross-backend switch here.

// BuildSurfaces builds the named backend's SurfaceSet from a run's shared inputs
// and a filesystem (nil = OS fs, for a test-injected afero.Fs), reading the
// descriptor's newSurfaces builder (registry.go). A backend that materializes no
// surfaces (acp) or an unregistered name returns an agent.EmptySurfaceSet, so a
// caller can iterate Deliveries() unconditionally.
func BuildSurfaces(name string, inputs agent.SurfaceInputs, fs afero.Fs) agent.SurfaceSet {
	if d, ok := descriptors[name]; ok && d.newSurfaces != nil {
		return d.newSurfaces(inputs, fs)
	}
	return agent.EmptySurfaceSet{}
}

// SurfacesFor returns a CAPABILITY-ONLY surface set for engine: enough to answer
// SupportedApproaches and DefaultApproach, and nothing more.
//
// It exists so help text and shell completion can describe an engine without
// assembling any content for it. Both are read-only questions about what an
// engine CAN do, asked before the user has chosen a profile — building real
// inputs to answer them would mean loading bundles to print a table.
//
// The returned set must not be delivered through: its inputs are empty, so any
// Deliver would write nothing. An unknown engine yields EmptySurfaceSet, which
// reports no approaches for every kind and renders as "no surface information"
// rather than as an engine with no surfaces.
func SurfacesFor(engine string) (agent.SurfaceSet, error) {
	if _, ok := descriptors[engine]; !ok {
		return nil, fmt.Errorf("unknown engine %q", engine)
	}
	return BuildSurfaces(engine, agent.SurfaceInputs{}, afero.NewMemMapFs()), nil
}

// wellKnownPlacement is a placeholder agent.Placement for building a SurfaceSet
// that only drives the well-known Deliveries() path (materialize, which lands
// native files at the cell's dir). claude's NewSurfaces binds an out-of-cwd
// placement for its RACE-SAFE variants (--append-system-prompt-file /
// --mcp-config / --settings scratch); those are never exercised on the well-known
// path, so the placement is never dereferenced and an empty dir is harmless. The
// other backends ignore the placement entirely (no out-of-cwd flag).
type wellKnownPlacement struct{}

// Dir returns the empty string: the well-known Deliveries() path never reads it.
func (wellKnownPlacement) Dir() string { return "" }

// UncarriedSurfaces is BuildSurfaces' inverse over the SAME inputs: the parts of
// a run's assembled loadout the named backend has NO structural place for. A
// delivery report can only list what it wrote — every line of it true — so the
// loss is invisible in it by construction; this is where a caller gets the other
// half (whiny-exclusive).
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
	d, ok := descriptors[name]
	if !ok || in.Hooks == nil {
		return nil
	}
	if d.noHooksReason != "" {
		detail := droppedHookDetail(name, *in.Hooks)
		if detail == "" {
			return nil
		}
		return []agent.SurfaceLoss{{Surface: "hooks", Detail: detail, Reason: d.noHooksReason}}
	}
	return unsupportedHookKindLosses(d, *in.Hooks)
}

// unsupportedHookKindLosses reports, for a backend that carries hooks
// generally but declares specific unified KINDS it has no native event for
// (agentDescriptor.unsupportedHookKinds), the ones the inputs actually
// configure — same "only when it costs something" rule as the whole-backend
// case above. Sorted by kind so the report is stable across a map's
// randomized range order.
func unsupportedHookKindLosses(d *agentDescriptor, hooks wire.HooksConfig) []agent.SurfaceLoss {
	if len(d.unsupportedHookKinds) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(d.unsupportedHookKinds))
	for k := range d.unsupportedHookKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var losses []agent.SurfaceLoss
	for _, kind := range kinds {
		n := len(unifiedEventHooks(hooks.Unified, kind))
		if n == 0 {
			continue
		}
		losses = append(losses, agent.SurfaceLoss{
			Surface: "hooks",
			Detail:  fmt.Sprintf("%d %s", n, kind),
			Reason:  d.unsupportedHookKinds[kind],
		})
	}
	return losses
}

// droppedHookDetail names what a hookless backend loses, in the user's own
// vocabulary: the six unified events by their config keys (in HookEvents order,
// so the line is stable run to run) plus any backend-native passthrough hooks
// addressed at THIS engine, which are equally undeliverable. "" when the config
// carries nothing.
func droppedHookDetail(name string, hooks wire.HooksConfig) string {
	var parts []string
	for _, event := range HookEvents() {
		if n := len(unifiedEventHooks(hooks.Unified, event)); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, event))
		}
	}
	var native []string
	for event, hs := range hooks.Plugins[name] {
		if len(hs) > 0 {
			native = append(native, fmt.Sprintf("%d %s", len(hs), event))
		}
	}
	sort.Strings(native) // map range order is random; a report that reshuffles cannot be diffed
	parts = append(parts, native...)
	return strings.Join(parts, ", ")
}
