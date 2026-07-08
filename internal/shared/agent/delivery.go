package agent

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file defines the pure type contracts for the runner-side delivery seam.
// A loadout is composed of surfaces (context, MCP, skills, settings). Each
// surface is delivered by a mechanism strategy (added in later slices) that
// returns a handle owning its own cleanup. The facet interfaces below are kept
// small so a strategy implements only the surface(s) it actually delivers.

// Delivered is the handle returned from delivering one surface of a loadout: it
// owns the cleanup that undoes that delivery. Each mechanism strategy returns a
// Delivered whose Cleanup reverses exactly what the strategy did — remove the
// written file, reconcile a settings edit, deregister an MCP server, or no-op
// for an in-band delivery that leaves nothing behind.
type Delivered interface {
	// Cleanup undoes the delivery this handle represents (rm file / reconcile /
	// deregister / no-op).
	Cleanup() error
}

// ContextDelivery delivers the context surface of a loadout — the model-facing
// instructions (the sysprompt / CLAUDE.md text). It is a small per-surface
// facet: a context mechanism strategy satisfies it on its own without having to
// implement the other surface facets.
type ContextDelivery interface {
	// DeliverContext materializes the assembled context text for the session and
	// returns a handle owning its cleanup.
	DeliverContext(context string) (Delivered, error)
}

// MCPDelivery delivers the MCP surface of a loadout: the merged MCP server set
// plus the servers shipped by profile and builtin bundles. It is a small
// per-surface facet an MCP mechanism strategy satisfies on its own.
type MCPDelivery interface {
	// DeliverMCP materializes the MCP configuration for the session and returns a
	// handle owning its cleanup. bundle carries the profile+builtin-bundle servers
	// (parallel to ManagedConfig.BundleMCP).
	DeliverMCP(mcp *wire.MCPConfig, bundle map[string]wire.MCPServer) (Delivered, error)
}

// SkillsDelivery delivers the skills surface of a loadout: the per-target-agent
// slash-command (skill) exports. It is a small per-surface facet a skills
// mechanism strategy satisfies on its own.
type SkillsDelivery interface {
	// DeliverSkills materializes the skill exports for the session and returns a
	// handle owning its cleanup.
	DeliverSkills(skills []CommandExport) (Delivered, error)
}

// SettingsDelivery delivers the settings surface of a loadout: the hook set
// (config + default-profile + bundle hooks, without context-injection) and
// whether ctxloom manages the engine statusline. It is a small per-surface
// facet a settings mechanism strategy satisfies on its own.
type SettingsDelivery interface {
	// DeliverSettings materializes the settings (hooks + statusline management)
	// for the session and returns a handle owning its cleanup.
	DeliverSettings(hooks *wire.HooksConfig, manageStatusline bool) (Delivered, error)
}

// DeliveryFactory builds the per-surface delivery strategies of a loadout, each
// targeted at a given Placement. A launch backend that routes its launch-time
// surface delivery through the seam injects one (via InitLaunch); Setup asks it
// for the strategy per surface. A factory returns nil for a surface it does not
// deliver through the seam, and Setup skips that surface. It is deliberately
// per-Placement: the caller owns WHERE each surface lands (ephemeral scratch vs
// the working dir) and the factory owns HOW (the concrete write mechanism).
type DeliveryFactory interface {
	// ContextDelivery returns the strategy that materializes the context surface
	// into place, or nil when the backend does not deliver context via the seam.
	ContextDelivery(place Placement) ContextDelivery
	// MCPDelivery returns the strategy that materializes the MCP surface into
	// place, or nil when the backend does not deliver MCP via the seam.
	MCPDelivery(place Placement) MCPDelivery
	// SkillsDelivery returns the strategy that materializes the skills surface
	// into place, or nil when the backend does not deliver skills via the seam.
	SkillsDelivery(place Placement) SkillsDelivery
	// SettingsDelivery returns the strategy that materializes the settings surface
	// into place, or nil when the backend does not deliver settings via the seam.
	SettingsDelivery(place Placement) SettingsDelivery
}

// Placement is where a file-writing mechanism strategy writes its surface. It is
// injected into the strategy at construction — never passed as a method
// parameter: a file/template strategy holds the Placement it writes into, while
// in-band and hook strategies hold none. Delivery is runner-side, so a Placement
// resolves to a runner-local, isolation-private directory.
type Placement interface {
	// Dir returns the directory the strategy writes into.
	Dir() string
}

// ephemeralPlacement writes into the session's regenerable ephemeral directory
// (~/.ctxloom/sessions/<harp>/ephemeral): the Placement for surfaces whose
// materialized files are session-scoped scratch that teardown may discard. When
// harp is empty or its ephemeral dir cannot be resolved, it falls back to the OS
// temp dir so a file-writing strategy always has a writable location.
type ephemeralPlacement struct {
	harp string
}

// Dir returns the harp's ephemeral directory, falling back to os.TempDir() when
// harp is empty or the ephemeral dir cannot be resolved.
func (p ephemeralPlacement) Dir() string {
	if p.harp != "" {
		if dir, err := paths.HarpEphemeralDir(p.harp); err == nil {
			return dir
		}
	}
	return os.TempDir()
}

// cwdPlacement writes into a fixed directory (typically the session's working
// directory): the Placement for surfaces a strategy must materialize where the
// engine already looks — e.g. a project-rooted config file — rather than in
// session-scoped scratch.
type cwdPlacement struct {
	dir string
}

// Dir returns the fixed directory this placement was constructed with.
func (p cwdPlacement) Dir() string {
	return p.dir
}
