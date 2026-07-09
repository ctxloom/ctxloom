package kiro

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands kiro's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go): each of kiro's surfaces as an object that
// implements agent.Delivery (the engine's well-known write). It is ADDITIVE:
// every surface object WRAPS an existing kiro writer verbatim — the ContextWriter
// core (KiroWriter.WriteContext → .kiro/steering/ctxloom-context.md), the shared
// MCP-file reconciler (mcpFile().WriteServers → .kiro/settings/mcp.json), the
// agent-config writer (mapHooks + writeAgentConfig → .kiro/agents/<name>.json),
// and the managed-command writer (WriteCommandFiles → .kiro/skills/). Nothing
// here is wired into the launch path (launch_backend.go / Setup / buildArgs);
// that cutover is Phase 2 (plan S4).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, "Per-engine
// race-safe coverage"): kiro's context, MCP, and skills surfaces are
// WELL-KNOWN-ONLY (plain agent.Delivery). kiro DOES have a per-agent lever — the
// settings + hooks live in .kiro/agents/<name>.json, selected at launch by
// `--agent <name>` — but that is NAME-based isolation resolved in buildArgs, a
// Phase-2 launch concern; at the delivery layer every kiro surface is still a
// well-known write into a project-rooted file, so ALL FOUR are modeled as plain
// agent.Delivery here. (Concurrent per-agent isolation via distinct `--agent`
// names is noted for Phase 2, not implemented as a RaceSafeDelivery.)
//
//	surface  | well-known target                       | also RaceSafeDelivery?
//	---------|-----------------------------------------|-----------------------
//	context  | .kiro/steering/ctxloom-context.md       | ❌ (steering, auto-loaded)
//	MCP      | .kiro/settings/mcp.json                 | ❌ no flag
//	settings | .kiro/agents/<name>.json (hooks)        | ❌ (--agent is Phase 2)
//	skills   | .kiro/skills/<name>/SKILL.md            | ❌ no flag
//
// kiro reads steering rather than firing a SessionStart hook for context (the
// context surface is a whole-file steering write, not a hook), and its hooks live
// inside the agent JSON (settings + hooks fold into one surface, as claude folds
// settings + hooks into settings.json).

// contextSurface is kiro's context surface: the ctxloom-owned steering file
// (.kiro/steering/ctxloom-context.md), written via the reused ContextWriter core
// (WriteContext). Delivery-ONLY.
type contextSurface struct {
	context string
	fs      afero.Fs
}

// Deliver writes the steering file via WriteContext and returns a handle whose
// Cleanup removes it (writing empty context).
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &KiroWriter{FS: s.fs}
	if _, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: dir, Context: s.context}); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error {
		_, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: dir, Context: ""})
		return err
	}), nil
}

// UnsafeInfo returns kiro's context identity for the Unsafe warning.
func (s *contextSurface) UnsafeInfo() string { return "kiro/context" }

// Kind reports kiro's context surface (.kiro/steering/ctxloom-context.md).
func (s *contextSurface) Kind() agent.SurfaceKind { return agent.SurfaceContext }

// mcpSurface is kiro's MCP surface: .kiro/settings/mcp.json, written via the
// shared MCP-file reconciler (mcpFile().WriteServers). Delivery-ONLY.
type mcpSurface struct {
	mcp       *wire.MCPConfig
	bundleMCP map[string]wire.MCPServer
	fs        afero.Fs
}

// Deliver writes .kiro/settings/mcp.json via the reused reconciler and returns a
// handle whose Cleanup drops the ctxloom-managed servers (RemoveServers).
func (s *mcpSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &KiroWriter{FS: s.fs}
	if err := w.mcpFile(dir).WriteServers(s.mcp, s.bundleMCP); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error { return w.mcpFile(dir).RemoveServers() }), nil
}

// UnsafeInfo returns kiro's MCP identity for the Unsafe warning.
func (s *mcpSurface) UnsafeInfo() string { return "kiro/mcp" }

// Kind reports kiro's MCP surface (.kiro/settings/mcp.json).
func (s *mcpSurface) Kind() agent.SurfaceKind { return agent.SurfaceMCP }

// settingsSurface is kiro's folded settings + hooks surface: the ctxloom-owned
// custom-agent config .kiro/agents/<name>.json, written via the reused mapHooks +
// writeAgentConfig. kiro's hooks live inside this agent JSON, so settings + hooks
// are one surface. Delivery-ONLY.
//
// NOTE (Phase 2): the agent's NAME (defaultAgentName) is what `--agent <name>`
// selects at launch — kiro's per-agent isolation lever. Writing distinct
// per-agent JSON files and passing the matching --agent is a buildArgs concern
// deferred to the launch-path cutover; this surface only materializes the file.
type settingsSurface struct {
	hooks *wire.HooksConfig
	fs    afero.Fs
}

// Deliver maps the hooks into kiro's agent-JSON hook block and writes
// .kiro/agents/<name>.json via the reused writer; Cleanup removes that
// ctxloom-owned file (mirroring RemoveSettings' agent-config portion). The
// context-injection hash mapHooks diverts is the context surface's concern
// (kiro delivers context via steering, not a hook), so it is ignored here.
func (s *settingsSurface) Deliver(dir string) (agent.Delivered, error) {
	hooks := s.hooks
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}
	w := &KiroWriter{FS: s.fs}
	_, agentHooks := w.mapHooks(hooks.Unified, hooks.Plugins["kiro"])
	if err := w.writeAgentConfig(dir, agentHooks); err != nil {
		return nil, err
	}
	fs := s.fs
	path := w.agentPath(dir)
	return deliveredFunc(func() error {
		if exists, _ := afero.Exists(fs, path); !exists {
			return nil
		}
		return fs.Remove(path)
	}), nil
}

// UnsafeInfo returns kiro's settings identity for the Unsafe warning.
func (s *settingsSurface) UnsafeInfo() string { return "kiro/settings" }

// Kind reports kiro's settings surface (.kiro/agents/<name>.json — hooks folded in).
func (s *settingsSurface) Kind() agent.SurfaceKind { return agent.SurfaceSettings }

// kiro's skills surface — the agentskills SKILL.md files under .kiro/skills/ — is
// the shared agent.ManagedSkillsDelivery bound to kiro's manifest-scoped
// WriteCommandFiles (built in NewSurfaces); its write-then-revert-with-nil shape
// is identical across engines, so it lives in internal/shared/agent, not here.

// deliveredFunc adapts a cleanup closure to agent.Delivered so a surface can
// return its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

// Surfaces is kiro's set of delivery surfaces for one run — four surface objects:
// context (steering), MCP (settings/mcp.json), settings (agent JSON + hooks), and
// skills (.kiro/skills/).
type Surfaces struct {
	Context  *contextSurface
	MCP      *mcpSurface
	Settings *settingsSurface
	Skills   *agent.ManagedSkillsDelivery
}

// NewSurfaces builds kiro's surfaces from a run's shared inputs (kiro uses the
// assembled context text, merged MCP + bundle servers, the hook set, and the
// skill exports). A nil fs defaults to the OS filesystem. Every kiro surface's
// Delivery takes its target dir at call time; none is modeled as race-safe here
// (the `--agent` lever is Phase 2).
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	return Surfaces{
		Context:  &contextSurface{context: in.Context, fs: fs},
		MCP:      &mcpSurface{mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs},
		Settings: &settingsSurface{hooks: in.Hooks, fs: fs},
		Skills: agent.NewManagedSkillsDelivery("kiro/skills", in.Skills, func(dir string, skills []agent.CommandExport) error {
			return WriteCommandFiles(dir, skills, agent.WithCommandFS(fs))
		}),
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe. This is the ONLY way
// kiro's surfaces reach a cell at the delivery layer: none is a RaceSafeDelivery,
// so a SharedCell can accept a kiro surface only through the loud agent.Unsafe
// adapter (see SharedCwdDeliveries). (The `--agent` name lever isolates CONCURRENT
// agents at launch — Phase 2 — orthogonally to this seam.)
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.MCP, s.Settings, s.Skills}
}

// SharedCwdDeliveries wraps every kiro surface in the loud agent.Unsafe adapter for
// a SharedCell (the user's live cwd) at dir. At the delivery layer kiro offers no
// out-of-cwd redirect for its files, so a shared cwd is the sanctioned last resort
// — an isolated cell (worktree/container) is always the first preference. Each
// returned value is a RaceSafeDelivery, so it is assignable to SharedCell.Deliver.
func (s Surfaces) SharedCwdDeliveries(dir string) []agent.RaceSafeDelivery {
	return []agent.RaceSafeDelivery{
		agent.Unsafe(s.Context, dir),
		agent.Unsafe(s.MCP, dir),
		agent.Unsafe(s.Settings, dir),
		agent.Unsafe(s.Skills, dir),
	}
}

// Compile-time capability contracts. Every kiro surface is Delivery-ONLY at this
// layer: none is assignable to agent.RaceSafeDelivery, the compile-time guarantee
// that no kiro surface can enter a SharedCell except through agent.Unsafe.
var (
	_ agent.UnsafeSurface  = (*contextSurface)(nil)
	_ agent.UnsafeSurface  = (*mcpSurface)(nil)
	_ agent.UnsafeSurface  = (*settingsSurface)(nil)
	_ agent.KindedDelivery = (*contextSurface)(nil)
	_ agent.KindedDelivery = (*mcpSurface)(nil)
	_ agent.KindedDelivery = (*settingsSurface)(nil)
	_ agent.Delivered      = deliveredFunc(nil)
	// Surfaces exposes both delivery sets (Deliveries + SharedCwdDeliveries), so
	// it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
