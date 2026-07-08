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
// implements agent.Delivery (the engine's well-known write). It is ADDITIVE:
// every surface object WRAPS an existing antigravity writer verbatim — the
// ContextWriter core (AntigravityHookWriter.WriteContext → .agents/AGENTS.md),
// the shared MCP-file reconciler (mcpFile().WriteServers → .agents/mcp_config.json),
// the hooks.json writer helpers WriteSettings composes, and the managed-command
// writer (WriteCommandFiles → .agents/skills/). Nothing here is wired into the
// launch path (launch_backend.go / Setup / buildArgs); that cutover is Phase 2
// (plan S4).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, "Per-engine
// race-safe coverage"): agy exposes NO out-of-cwd redirect (no --mcp-config /
// --settings / --append-system-prompt equivalent), so EVERY antigravity surface
// is WELL-KNOWN-ONLY — a plain agent.Delivery, never a agent.RaceSafeDelivery.
// Per-agent CONCURRENT isolation for agy therefore requires a private cwd
// (worktree) or container cell, never a SharedCell.
//
//	surface  | well-known target             | also RaceSafeDelivery?
//	---------|-------------------------------|-----------------------
//	context  | .agents/AGENTS.md             | ❌ no flag
//	MCP      | .agents/mcp_config.json       | ❌ no flag
//	hooks    | .agents/hooks.json            | ❌ no flag
//	skills   | .agents/skills/<name>.md      | ❌ no flag
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
// nothing user-authored remains) by writing empty context.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &AntigravityHookWriter{FS: s.fs}
	if _, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: dir, Context: s.context}); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error {
		_, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: dir, Context: ""})
		return err
	}), nil
}

// UnsafeInfo returns agy's context identity for the Unsafe warning.
func (s *contextSurface) UnsafeInfo() string { return "antigravity/context" }

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

// UnsafeInfo returns agy's MCP identity for the Unsafe warning.
func (s *mcpSurface) UnsafeInfo() string { return "antigravity/mcp" }

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

// UnsafeInfo returns agy's hooks identity for the Unsafe warning.
func (s *hooksSurface) UnsafeInfo() string { return "antigravity/hooks" }

// agy's skills surface — the markdown skill files under .agents/skills/ — is the
// shared agent.ManagedSkillsDelivery bound to agy's manifest-scoped
// WriteCommandFiles (built in NewSurfaces); its write-then-revert-with-nil shape
// is identical across engines, so it lives in internal/shared/agent, not here.

// deliveredFunc adapts a cleanup closure to agent.Delivered so a surface can
// return its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

// SurfaceInputs carries the per-run data agy's surfaces write. It mirrors what
// the launch path already assembles — the assembled context text, the merged MCP
// config + profile/builtin bundle servers, the hook set, and the skill exports.
// S1 only defines and fills it in tests; Phase 2 (S4) feeds it from Setup.
type SurfaceInputs struct {
	Context   string
	MCP       *wire.MCPConfig
	BundleMCP map[string]wire.MCPServer
	Hooks     *wire.HooksConfig
	Skills    []agent.CommandExport
}

// Surfaces is agy's set of delivery surfaces for one run — four surface objects,
// one per .agents/ file: context (AGENTS.md), MCP (mcp_config.json), hooks
// (hooks.json), and skills (skills/).
type Surfaces struct {
	Context *contextSurface
	MCP     *mcpSurface
	Hooks   *hooksSurface
	Skills  *agent.ManagedSkillsDelivery
}

// NewSurfaces builds agy's surfaces from a run's inputs. A nil fs defaults to the
// OS filesystem. Every agy surface's Delivery takes its target dir at call time;
// none is race-safe (agy exposes no out-of-cwd flag), so there is no isolated
// placement to bind.
func NewSurfaces(in SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	return Surfaces{
		Context: &contextSurface{context: in.Context, fs: fs},
		MCP:     &mcpSurface{mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs},
		Hooks:   &hooksSurface{hooks: in.Hooks, fs: fs},
		Skills: agent.NewManagedSkillsDelivery("antigravity/skills", in.Skills, func(dir string, skills []agent.CommandExport) error {
			return WriteCommandFiles(dir, skills, agent.WithCommandFS(fs))
		}),
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe. This is the ONLY way agy's
// surfaces reach a cell: none is race-safe, so a SharedCell can accept an agy
// surface only through the loud agent.Unsafe adapter (see SharedCwdDeliveries).
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.MCP, s.Hooks, s.Skills}
}

// SharedCwdDeliveries wraps every agy surface in the loud agent.Unsafe adapter for
// a SharedCell (the user's live cwd) at dir. agy offers no out-of-cwd flag for
// ANY surface, so a shared cwd is the sanctioned last resort — an isolated cell
// (worktree/container) is always the first preference. Each returned value is a
// RaceSafeDelivery, so it is assignable to SharedCell.Deliver.
func (s Surfaces) SharedCwdDeliveries(dir string) []agent.RaceSafeDelivery {
	return []agent.RaceSafeDelivery{
		agent.Unsafe(s.Context, dir),
		agent.Unsafe(s.MCP, dir),
		agent.Unsafe(s.Hooks, dir),
		agent.Unsafe(s.Skills, dir),
	}
}

// Compile-time capability contracts. Every agy surface is Delivery-ONLY: none is
// assignable to agent.RaceSafeDelivery, the compile-time guarantee that no agy
// surface can enter a SharedCell except through agent.Unsafe.
var (
	_ agent.UnsafeSurface = (*contextSurface)(nil)
	_ agent.UnsafeSurface = (*mcpSurface)(nil)
	_ agent.UnsafeSurface = (*hooksSurface)(nil)
	_ agent.Delivered     = deliveredFunc(nil)
	// Surfaces exposes both delivery sets (Deliveries + SharedCwdDeliveries), so
	// it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
