package antigravity

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands antigravity's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go): each of agy's surfaces as an object that
// implements agent.Delivery (the engine's well-known write). Every surface object
// WRAPS an existing antigravity writer verbatim — the ContextWriter core
// (AntigravityHookWriter.WriteContext → .agents/AGENTS.md), the shared MCP-file
// reconciler (mcpFile().WriteServers → .agents/mcp_config.json), the hooks.json
// writer helpers WriteSettings composes, and the managed-command writer
// (WriteCommandFiles → .agents/skills/).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, "Per-engine
// race-safe coverage"): agy exposes NO out-of-cwd redirect (no --mcp-config /
// --settings / --append-system-prompt equivalent) and NO SharedRealization (see
// Surfaces.SharedRealization below), so EVERY antigravity surface is
// WELL-KNOWN-ONLY. Per-agent CONCURRENT isolation for agy therefore requires a
// private cwd (worktree) or container cell; a SHARED-cwd delivery falls back to
// the loud well-known write.
//
//	surface  | well-known target             | also a SharedRealization?
//	---------|-------------------------------|-----------------------
//	context  | .agents/AGENTS.md             | ❌ no flag
//	MCP      | .agents/mcp_config.json       | ❌ no flag
//	hooks    | .agents/hooks.json            | ❌ no flag
//	commands | .agents/skills/<name>.md      | ❌ no flag
//
// Unlike claude (which folds settings + hooks into one .claude/settings.json),
// agy keeps hooks, MCP, and context in three separate files under .agents/, so
// each is its own surface. agy has no SessionStart hook for context — it reads
// .agents/AGENTS.md at session start — which is why the context surface is a
// whole-file write, not a hook. The hooks surface reuses the SAME hooks.json
// helpers WriteSettings composes (load → remove-managed → add), minus the
// context-reconcile and MCP write those other surfaces own; the context hash
// addUnifiedHooks returns is deliberately ignored (context is the context
// surface's job).

// contextSurface is agy's context surface: the ctxloom-managed section of
// .agents/AGENTS.md, written via the reused ContextWriter core (WriteContext).
// Delivery-ONLY — agy has no out-of-cwd context flag.
type contextSurface struct {
	context string
	fs      afero.Fs
}

// Deliver merges the context into .agents/AGENTS.md via WriteContext and returns
// a handle whose Cleanup strips the managed section (removing the file when
// nothing user-authored remains) by writing empty context. This is the shared
// agent.DeliverManagedContext shape, the same one claude's CLAUDE.md and
// codex's AGENTS.md context surfaces use.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&AntigravityHookWriter{FS: s.fs}, dir, s.context)
}

// UnsafeInfo returns agy's context identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *contextSurface) UnsafeInfo() string { return "antigravity/context" }

// Kind reports agy's context surface (.agents/AGENTS.md).
func (s *contextSurface) Kind() agent.SurfaceKind { return agent.SurfaceContext }

// mcpSurface is agy's MCP surface: .agents/mcp_config.json, written via the
// shared MCP-file reconciler (mcpFile().WriteServers). Delivery-ONLY.
type mcpSurface struct {
	mcp       *wire.MCPConfig
	bundleMCP map[string]wire.MCPServer
	fs        afero.Fs
}

// Deliver writes .agents/mcp_config.json via the reused reconciler and returns a
// handle whose Cleanup drops the ctxloom-managed servers (RemoveServers), leaving
// user entries intact.
func (s *mcpSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &AntigravityHookWriter{FS: s.fs}
	if err := w.mcpFile(dir).WriteServers(s.mcp, s.bundleMCP); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error { return w.mcpFile(dir).RemoveServers() }), nil
}

// UnsafeInfo returns agy's MCP identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *mcpSurface) UnsafeInfo() string { return "antigravity/mcp" }

// Kind reports agy's MCP surface (.agents/mcp_config.json).
func (s *mcpSurface) Kind() agent.SurfaceKind { return agent.SurfaceMCP }

// hooksSurface is agy's hooks surface: the ctxloom-managed entries of
// .agents/hooks.json. It reuses the SAME writer helpers WriteSettings composes
// for the hooks.json portion (loadHooksFile → removeCtxloomHooks →
// addUnifiedHooks → addBackendHooks → saveHooksFile). Delivery-ONLY.
type hooksSurface struct {
	hooks *wire.HooksConfig
	fs    afero.Fs
}

// Deliver writes the managed hooks into .agents/hooks.json via the reused helpers
// and returns a handle whose Cleanup reverts them (load → remove-managed → save),
// mirroring RemoveSettings' hooks portion.
func (s *hooksSurface) Deliver(dir string) (agent.Delivered, error) {
	hooks := s.hooks
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}
	w := &AntigravityHookWriter{FS: s.fs}
	hooksPath := w.SettingsPath(dir)
	if err := s.fs.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create %s directory: %w", AgentsDir, err)
	}
	hf, err := w.loadHooksFile(hooksPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load existing hooks.json: %w", err)
	}
	w.removeCtxloomHooks(hf)
	// The context-injection hash addUnifiedHooks diverts is the context surface's
	// concern (agy delivers context via AGENTS.md, not a hook); ignore it here.
	_ = w.addUnifiedHooks(hf, hooks.Unified)
	if backendHooks, ok := hooks.Plugins["antigravity"]; ok {
		w.addBackendHooks(hf, backendHooks)
	}
	if err := w.saveHooksFile(hooksPath, hf); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error {
		hf, err := w.loadHooksFile(hooksPath)
		if err != nil {
			return err
		}
		w.removeCtxloomHooks(hf)
		return w.saveHooksFile(hooksPath, hf)
	}), nil
}

// UnsafeInfo returns agy's hooks identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *hooksSurface) UnsafeInfo() string { return "antigravity/hooks" }

// Kind reports agy's hooks surface (.agents/hooks.json) as the settings surface —
// agy's hooks file IS its settings, so a caller selecting Settings gets it.
func (s *hooksSurface) Kind() agent.SurfaceKind { return agent.SurfaceSettings }

// agy's commands surface — the markdown skill files under .agents/skills/ — is
// the shared agent.ManagedCommandsDelivery bound to agy's manifest-scoped
// WriteCommandFiles (built in NewSurfaces); its write-then-revert-with-nil shape
// is identical across engines, so it lives in internal/shared/agent, not here.

// deliveredFunc adapts a cleanup closure to agent.Delivered so a surface can
// return its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

// Surfaces is agy's set of delivery surfaces for one run — four surface objects,
// one per .agents/ file: context (AGENTS.md), MCP (mcp_config.json), hooks
// (hooks.json), and commands (skills/).
type Surfaces struct {
	Context  *contextSurface
	MCP      *mcpSurface
	Hooks    *hooksSurface
	Commands *agent.ManagedCommandsDelivery

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once
	// here (not reallocated per SurfaceFor call) since it never changes after
	// construction.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds agy's surfaces from a run's shared inputs (agy uses the
// assembled context text, merged MCP + bundle servers, the hook set, and the
// command exports; Fragments/ManageStatusline are for other engines). A nil fs
// defaults to the OS filesystem. Every agy surface's Delivery takes its target
// dir at call time; none is race-safe (agy exposes no out-of-cwd flag), so there
// is no isolated placement to bind.
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	context := &contextSurface{context: in.Context, fs: fs}
	mcp := &mcpSurface{mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs}
	hooks := &hooksSurface{hooks: in.Hooks, fs: fs}
	commands := agent.NewManagedCommandsDelivery("antigravity/commands", in.Commands, func(dir string, commands []agent.CommandExport) error {
		return WriteCommandFiles(dir, commands, agent.WithCommandFS(fs))
	})
	return Surfaces{
		Context:  context,
		MCP:      mcp,
		Hooks:    hooks,
		Commands: commands,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  context,
			agent.SurfaceMCP:      mcp,
			agent.SurfaceSettings: hooks,
			agent.SurfaceCommands: commands,
		},
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe. This is the ONLY way agy's
// surfaces reach a cell directly: none has a SharedRealization, so a SHARED-cwd
// delivery falls back to the loud well-known write (see Surfaces.SharedRealization
// below).
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.MCP, s.Hooks, s.Commands}
}

// agyApproaches is agy's DECLARED per-surface approach table (vital-tiger v2
// per-provider dispatch): every surface is a single native file — agy has no
// out-of-cwd flag and no SessionStart hook (it reads .agents/AGENTS.md directly).
// The mechanical lookups ride agent.ApproachTable; only this table is agy's.
var agyApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachUnsafeFile},
	agent.SurfaceMCP:      {agent.ApproachUnsafeFile},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceCommands: {agent.ApproachUnsafeFile},
}

// SupportedApproaches reports agy's declared approach table for kind.
func (Surfaces) SupportedApproaches(kind agent.SurfaceKind) []agent.Approach {
	return agyApproaches.Supported(kind)
}

// DefaultApproach reports agy's default (first-declared: the native file)
// approach for kind.
func (Surfaces) DefaultApproach(kind agent.SurfaceKind) (agent.Approach, bool) {
	return agyApproaches.Default(kind)
}

// SurfaceFor resolves one (kind, UnsafeFile) to the concrete agy surface via
// the shared table lookup against s.dispatch (built once in NewSurfaces, not
// reallocated per call). agy's hooks file IS its settings surface. Any other
// approach is unsupported.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return agyApproaches.SurfaceFor("antigravity", s.dispatch, kind, a)
}

// SharedRealization reports no realization for any kind: agy has no out-of-cwd
// redirect for ANY surface, so a SHARED-cwd delivery always falls back to the
// loud well-known write.
func (Surfaces) SharedRealization(agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts. Every agy surface is a KindedDelivery.
var (
	_ agent.KindedDelivery = (*contextSurface)(nil)
	_ agent.KindedDelivery = (*mcpSurface)(nil)
	_ agent.KindedDelivery = (*hooksSurface)(nil)
	_ agent.Delivered      = deliveredFunc(nil)
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
