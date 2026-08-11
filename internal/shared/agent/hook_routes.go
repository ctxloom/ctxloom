package agent

import (
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// HookRoute maps one unified hook slice onto an agent-native event name. The
// per-agent settings writers all share this translation skeleton — only the
// native event vocabulary, default matchers, and route order differ — so each
// declares its routes as data and RouteUnifiedHooks does the walk. Hooks that
// need per-agent special handling are handled by the agent before routing
// and simply omitted from its route table.
type HookRoute struct {
	// Hooks is the unified slice this route emits (e.g. unified.PreShell).
	Hooks []wire.Hook
	// Event is the agent-native event name to emit under. Empty is only valid
	// together with Unsupported.
	Event string
	// DefaultMatcher is applied when a hook carries no matcher of its own
	// (e.g. scoping unified PreShell to the agent's shell tools).
	DefaultMatcher string

	// Kind names the unified hook kind in the user's vocabulary
	// ("session_end") — the config key they wrote. Only needed on an
	// Unsupported route, where it is what the diagnostic must say back to them.
	Kind string
	// Unsupported declares that this agent has NO native event for the kind and
	// says why, in one clause ("codex has no session-end event"). Such a route
	// emits nothing — but if the user configured hooks of that kind, they are
	// silently inert, which is exactly what an OMITTED route used to look like
	// and why the omission could not be told apart from an oversight. Declaring
	// it makes the drop deliberate and, when it actually costs the user
	// something, audible.
	Unsupported string
}

// RouteUnifiedHooks emits every hook of every route, in route order, applying
// the route's default matcher to hooks without one. emit receives a copy of
// the hook, so applying the default never mutates the caller's config.
//
// engine names the agent for diagnostics. A route marked Unsupported emits
// nothing and warns once naming the engine, the kind, and the reason — but only
// when hooks of that kind were actually configured: a gap nobody asked to use
// costs nothing and stays quiet.
func RouteUnifiedHooks(engine string, routes []HookRoute, emit func(event string, h wire.Hook)) {
	for _, r := range routes {
		if r.Unsupported != "" {
			if len(r.Hooks) > 0 {
				clidiag.WarnOnce("ctxloom", "%s: %d unified %s hook(s) are configured but will not run — %s; they are written nowhere for this engine",
					engine, len(r.Hooks), r.Kind, r.Unsupported)
			}
			continue
		}
		for _, h := range r.Hooks {
			if h.Matcher == "" && r.DefaultMatcher != "" {
				h.Matcher = r.DefaultMatcher
			}
			emit(r.Event, h)
		}
	}
}
