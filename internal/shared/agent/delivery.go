package agent

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file defines the pure type contracts for the runner-side delivery seam.
// A loadout is composed of surfaces (context, MCP, commands, settings). Each
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

// MCPDelivery delivers the MCP surface of a loadout: the servers shipped by
// builtin, companion and profile bundles. It is a small per-surface facet an
// MCP mechanism strategy satisfies on its own.
type MCPDelivery interface {
	// DeliverMCP materializes the MCP configuration for the session and returns a
	// handle owning its cleanup. bundle carries the resolved bundle servers
	// (ManagedConfig.BundleMCP).
	DeliverMCP(bundle map[string]wire.MCPServer) (Delivered, error)
}

// CommandsDelivery delivers the commands surface of a loadout: the
// per-target-agent slash-command exports. It is a small per-surface facet a
// commands mechanism strategy satisfies on its own.
type CommandsDelivery interface {
	// DeliverCommands materializes the command exports for the session and
	// returns a handle owning its cleanup.
	DeliverCommands(commands []CommandExport) (Delivered, error)
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

// A fixed-directory Placement (writing into the session's working directory
// rather than session-scoped scratch) used to live here as cwdPlacement, but
// it was test-only: every backend that needs this shape declares its own
// (claude's dirPlacement, surfaces.go), so there was no real shared consumer
// to keep it for.
