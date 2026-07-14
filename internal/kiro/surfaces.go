package kiro

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands kiro's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go): each of kiro's surfaces as an object that
// implements agent.Delivery (the engine's well-known write). Every surface object
// WRAPS an existing kiro writer verbatim — the ContextWriter core
// (KiroWriter.WriteContext → .kiro/steering/ctxloom-context.md), the shared
// MCP-file reconciler (mcpFile().WriteServers → .kiro/settings/mcp.json), the
// agent-config writer (mapHooks + writeAgentConfig → .kiro/agents/<name>.json),
// and the managed-command writer (WriteCommandFiles → .kiro/skills/).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, "Per-engine
// race-safe coverage"): kiro's context, MCP, and skills surfaces are
// WELL-KNOWN-ONLY (plain agent.Delivery). kiro DOES have a per-agent lever — the
// settings + hooks live in .kiro/agents/<name>.json, selected at launch by
// `--agent <name>` — but that is NAME-based isolation resolved in buildArgs, a
// launch concern orthogonal to this seam; at the delivery layer every kiro
// surface is still a well-known write into a project-rooted file, so ALL FOUR are
// modeled as plain agent.Delivery here (none has a SharedRealization).
//
//	surface  | well-known target                       | also a SharedRealization?
//	---------|-----------------------------------------|-----------------------
//	context  | .kiro/steering/ctxloom-context.md       | ❌ (steering, auto-loaded)
//	MCP      | .kiro/settings/mcp.json                 | ❌ no flag
//	settings | .kiro/agents/<name>.json (hooks)        | ❌ (--agent is orthogonal)
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
// Cleanup removes it (writing empty context). This is the shared
// agent.DeliverManagedContext shape, the same one antigravity's AGENTS.md,
// claude's CLAUDE.md, and codex's AGENTS.md context surfaces use.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&KiroWriter{FS: s.fs}, dir, s.context)
}

// UnsafeInfo returns kiro's context identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
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

// UnsafeInfo returns kiro's MCP identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
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

// UnsafeInfo returns kiro's settings identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
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

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once
	// here (not reallocated per SurfaceFor call) since it never changes after
	// construction.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds kiro's surfaces from a run's shared inputs (kiro uses the
// assembled context text, merged MCP + bundle servers, the hook set, and the
// skill exports). A nil fs defaults to the OS filesystem. Every kiro surface's
// Delivery takes its target dir at call time; none is modeled as race-safe here
// (the `--agent` lever is Phase 2).
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	context := &contextSurface{context: in.Context, fs: fs}
	mcp := &mcpSurface{mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs}
	settings := &settingsSurface{hooks: in.Hooks, fs: fs}
	skills := agent.NewManagedSkillsDelivery("kiro/skills", in.Skills, func(dir string, skills []agent.CommandExport) error {
		return WriteCommandFiles(dir, skills, agent.WithCommandFS(fs))
	})
	return Surfaces{
		Context:  context,
		MCP:      mcp,
		Settings: settings,
		Skills:   skills,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  context,
			agent.SurfaceMCP:      mcp,
			agent.SurfaceSettings: settings,
			agent.SurfaceSkills:   skills,
		},
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe. This is the ONLY way
// kiro's surfaces reach a cell directly: none has a SharedRealization, so a
// SHARED-cwd delivery falls back to the loud well-known write (see
// Surfaces.SharedRealization below). (The `--agent` name lever isolates
// CONCURRENT agents at launch, orthogonally to this seam.)
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.MCP, s.Settings, s.Skills}
}

// kiroApproaches is kiro's DECLARED per-surface approach table (vital-tiger v2
// per-provider dispatch): every surface is a single native file. kiro reads
// .kiro/steering for context (no hook, no out-of-cwd flag), and its hooks live
// inside the agent JSON (settings + hooks fold into one surface). The mechanical
// lookups ride agent.ApproachTable; only this table is kiro's.
var kiroApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachUnsafeFile},
	agent.SurfaceMCP:      {agent.ApproachUnsafeFile},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceSkills:   {agent.ApproachUnsafeFile},
}

// SupportedApproaches reports kiro's declared approach table for kind.
func (Surfaces) SupportedApproaches(kind agent.SurfaceKind) []agent.Approach {
	return kiroApproaches.Supported(kind)
}

// DefaultApproach reports kiro's default (first-declared: the native file)
// approach for kind.
func (Surfaces) DefaultApproach(kind agent.SurfaceKind) (agent.Approach, bool) {
	return kiroApproaches.Default(kind)
}

// SurfaceFor resolves one (kind, UnsafeFile) to the concrete kiro surface via
// the shared table lookup against s.dispatch (built once in NewSurfaces, not
// reallocated per call). kiro's agent JSON IS its settings surface, hooks
// folded in. Any other approach is unsupported — WithContext(Hook) on kiro
// fails loudly, matching kiro's steering (not hook) context.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return kiroApproaches.SurfaceFor("kiro", s.dispatch, kind, a)
}

// SharedRealization reports no realization for any kind: kiro has no out-of-cwd
// redirect for ANY surface, so a SHARED-cwd delivery always falls back to the
// loud well-known write. (The `--agent` name lever isolates CONCURRENT agents at
// launch, orthogonally to this seam.)
func (Surfaces) SharedRealization(agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts. Every kiro surface is a KindedDelivery.
var (
	_ agent.KindedDelivery = (*contextSurface)(nil)
	_ agent.KindedDelivery = (*mcpSurface)(nil)
	_ agent.KindedDelivery = (*settingsSurface)(nil)
	_ agent.Delivered      = deliveredFunc(nil)
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
