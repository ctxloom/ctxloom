package agent

import "github.com/ctxloom/ctxloom/internal/shared/wire"

// HookRoute maps one unified hook slice onto an agent-native event name. The
// per-agent settings writers all share this translation skeleton — only the
// native event vocabulary, default matchers, and route order differ — so each
// declares its routes as data and RouteUnifiedHooks does the walk. Hooks that
// need per-agent special handling (e.g. antigravity's SessionStart context /
// PreToolUse-fallback diversion) are handled by the agent before routing and
// simply omitted from its route table.
type HookRoute struct {
	// Hooks is the unified slice this route emits (e.g. unified.PreShell).
	Hooks []wire.Hook
	// Event is the agent-native event name to emit under.
	Event string
	// DefaultMatcher is applied when a hook carries no matcher of its own
	// (e.g. scoping unified PreShell to the agent's shell tools).
	DefaultMatcher string
}

// RouteUnifiedHooks emits every hook of every route, in route order, applying
// the route's default matcher to hooks without one. emit receives a copy of
// the hook, so applying the default never mutates the caller's config.
func RouteUnifiedHooks(routes []HookRoute, emit func(event string, h wire.Hook)) {
	for _, r := range routes {
		for _, h := range r.Hooks {
			if h.Matcher == "" && r.DefaultMatcher != "" {
				h.Matcher = r.DefaultMatcher
			}
			emit(r.Event, h)
		}
	}
}
