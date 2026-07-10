package codex

import (
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands codex's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go): each of codex's surfaces as an object that
// implements agent.Delivery (the engine's well-known write). Every surface object
// WRAPS an existing codex writer verbatim — the shared context-file writer
// (agent.WriteContextFile), the config.toml writer (CodexHookWriter.WriteSettings),
// and the managed-command writer (agent.WriteManagedCommandFiles +
// codexPromptFile).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, "Per-engine
// race-safe coverage"): codex exposes NO out-of-cwd redirect (no
// --mcp-config / --settings / --append-system-prompt equivalent) and NO
// SharedRealization (see Surfaces.SharedRealization below), so EVERY codex
// surface is WELL-KNOWN-ONLY. Per-agent CONCURRENT isolation for codex therefore
// requires a private cwd (worktree) or container cell; a SHARED-cwd delivery
// falls back to the loud well-known write.
//
//	surface  | well-known target                         | also a SharedRealization?
//	---------|-------------------------------------------|-----------------------
//	context  | .ctxloom/cache/context/<hash>.md (file)   | ❌ no flag
//	config   | .codex/config.toml (hooks + MCP)          | ❌ no flag
//	skills   | $CODEX_HOME/prompts/<name>.md             | ❌ no flag
//
// Two codex realities shape the decomposition:
//
//  1. config.toml is FOLDED. codex has a single atomic writer (WriteSettings)
//     that owns the [hooks] AND [mcp_servers] tables of one file. Splitting MCP
//     from hooks would mean two partial load-modify-save passes over the same
//     file — so, exactly as claude folds "settings + hooks" into one surface,
//     codex folds "settings + hooks + MCP" into one config surface (its
//     SupportedApproaches reports SurfaceMCP absent, keying the fold at the
//     SurfaceSelection builder level too).
//
//  2. context is a FILE, read via a hook. codex has no ContextWriter; its
//     context reaches the model as a raw context file (agent.WriteContextFile)
//     that a SessionStart hook in config.toml reads. The contextSurface here
//     writes ONLY that file — the SessionStart context-injection hook that
//     consumes it is one of the hooks the config surface delivers. This mirrors
//     the existing Setup split (BaseContextProvider.Provide writes the file;
//     BaseLifecycle.Flush → WriteSettings writes config.toml with the hook).
//     codex's context surface is therefore Hook-ONLY (SupportedApproaches),
//     unlike claude's Hook (a documented no-op).
//
//  3. skills are GLOBAL. codex discovers prompts only from $CODEX_HOME/prompts
//     (default ~/.codex/prompts) — NOT cwd-relative — so an isolated *directory*
//     would not isolate them. The skillsSurface therefore writes prompts under a
//     CELL-SCOPED $CODEX_HOME derived from the delivery dir (<dir>/.codex/prompts,
//     i.e. CODEX_HOME=<dir>/.codex), so a DirectoryIsolatedCell genuinely
//     isolates them. Exporting CODEX_HOME=<dir>/.codex to the launched codex is a
//     buildArgs/env concern.

// cellScopedCodexHome derives a per-cell $CODEX_HOME from the delivery dir. In an
// isolated cell (worktree / container mount) at dir, <dir>/.codex is both the
// project config dir codex reads AND the CODEX_HOME its global prompts/sessions
// hang off — so pointing CODEX_HOME here keeps prompts cell-local. It matches
// codexHome()'s output when CODEX_HOME=<dir>/.codex (CODEX_HOME IS the .codex
// dir, not its parent), so codexPromptsDir would resolve to cellScopedPromptsDir.
func cellScopedCodexHome(dir string) string { return filepath.Join(dir, ".codex") }

// cellScopedPromptsDir is the prompts directory under the cell-scoped
// $CODEX_HOME — the isolated stand-in for the global codexPromptsDir().
func cellScopedPromptsDir(dir string) string {
	return filepath.Join(cellScopedCodexHome(dir), "prompts")
}

// contextSurface is codex's context surface. codex has no ContextWriter: its
// context is a raw file (agent.WriteContextFile) that a SessionStart hook in
// config.toml reads. Deliver writes that file into dir's well-known context
// cache; the SessionStart hook that consumes it is delivered by the config
// surface (it is one of the hooks). Delivery-ONLY — codex has no out-of-cwd
// context flag.
type contextSurface struct {
	fragments []*agent.Fragment
	fs        afero.Fs
}

// Deliver writes the assembled context file into dir via agent.WriteContextFile
// (the same writer BaseContextProvider.Provide uses) and returns a handle whose
// Cleanup removes it. Empty/absent content writes nothing and returns a NIL handle
// (there is nothing to clean up and nothing was delivered) — which is how a caller
// distinguishes "codex wrote a context file" from "no-op", e.g. materialize (no
// fragments) reports no context surface for codex.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	hash, err := agent.WriteContextFile(dir, s.fragments, agent.WithContextFS(s.fs))
	if err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, nil
	}
	fs := s.fs
	path := filepath.Join(dir, agent.SCMContextSubdir, hash+".md")
	return deliveredFunc(func() error { return fs.Remove(path) }), nil
}

// UnsafeInfo returns codex's context identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *contextSurface) UnsafeInfo() string { return "codex/context" }

// Kind reports codex's context surface (the raw context cache file).
func (s *contextSurface) Kind() agent.SurfaceKind { return agent.SurfaceContext }

// configSurface is codex's folded settings + hooks + MCP surface: the single
// .codex/config.toml written by CodexHookWriter.WriteSettings, which owns the
// [hooks] and [mcp_servers] tables together. Delivery-ONLY — codex has no
// per-invocation config redirect.
type configSurface struct {
	hooks     *wire.HooksConfig
	mcp       *wire.MCPConfig
	bundleMCP map[string]wire.MCPServer
	fs        afero.Fs
}

// Deliver writes .codex/config.toml into dir via the reused WriteSettings and
// returns a handle whose Cleanup reverts the ctxloom-managed hooks and servers
// via RemoveSettings (user keys preserved).
func (s *configSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &CodexHookWriter{FS: s.fs}
	if err := w.WriteSettings(s.hooks, s.mcp, s.bundleMCP, dir); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error { return w.RemoveSettings(dir) }), nil
}

// UnsafeInfo returns codex's config identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *configSurface) UnsafeInfo() string { return "codex/config" }

// Kind reports codex's config surface as the settings surface — it folds codex's
// [hooks] AND [mcp_servers] tables into one config.toml, so a caller selecting
// either Settings or MCP gets the whole file.
func (s *configSurface) Kind() agent.SurfaceKind { return agent.SurfaceSettings }

// codex's prompts surface is the shared agent.ManagedSkillsDelivery bound to
// codex's writer (built in NewSurfaces). codex prompts are GLOBAL
// ($CODEX_HOME/prompts), so the bound writer targets a CELL-SCOPED $CODEX_HOME
// derived from the delivery dir (cellScopedPromptsDir) via the shared
// manifest-scoped writer + codexPromptFile mapper — that cell-scoping is what
// lets a DirectoryIsolatedCell isolate them. The write-then-revert-with-nil shape
// is identical across engines, so it lives in internal/shared/agent, not here.

// deliveredFunc adapts a cleanup closure to agent.Delivered so a surface can
// return its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

// Surfaces is codex's set of delivery surfaces for one run. codex has three
// surface objects — context (the raw context file), config (config.toml's folded
// hooks + MCP), and skills (cell-scoped prompts).
type Surfaces struct {
	Context *contextSurface
	Config  *configSurface
	Skills  *agent.ManagedSkillsDelivery
}

// NewSurfaces builds codex's surfaces from a run's shared inputs. codex's context
// is a raw file, so it takes the Fragments (not the assembled Context string); it
// also uses the merged MCP + bundle servers, the hook set, and the skill exports.
// A nil fs defaults to the OS filesystem. Every codex surface's Delivery takes its
// target dir at call time; none is race-safe (codex exposes no out-of-cwd flag),
// so there is no isolated placement to bind.
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	return Surfaces{
		Context: &contextSurface{fragments: in.Fragments, fs: fs},
		Config:  &configSurface{hooks: in.Hooks, mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs},
		Skills: agent.NewManagedSkillsDelivery("codex/skills (global $CODEX_HOME)", in.Skills, func(dir string, skills []agent.CommandExport) error {
			return agent.WriteManagedCommandFiles(fs, cellScopedPromptsDir(dir), codexManifest, skills, codexPromptFile)
		}),
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe. This is the ONLY way
// codex's surfaces reach a cell directly: none has a SharedRealization, so a
// SHARED-cwd delivery falls back to the loud well-known write (see
// Surfaces.SharedRealization below).
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.Config, s.Skills}
}

// codexApproaches is codex's DECLARED per-surface approach table (vital-tiger v2
// per-provider dispatch). codex has NO CLAUDE.md-style native context file — its
// context reaches the model only via the SessionStart inject-context hook reading
// the raw cache file — so context is Hook-ONLY (naming UnsafeFile is an
// unsupported combo the builder rejects). settings/skills are native-file-only.
// SurfaceMCP is deliberately ABSENT: MCP folds into the config/settings surface,
// so codex advertises no distinct MCP surface — selecting MCP is a permitted
// no-op, resolved by whichever selection also names Settings. The mechanical
// lookups ride agent.ApproachTable; only this table is codex's.
var codexApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachHook},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceSkills:   {agent.ApproachUnsafeFile},
}

// SupportedApproaches reports codex's declared approach table for kind.
func (Surfaces) SupportedApproaches(kind agent.SurfaceKind) []agent.Approach {
	return codexApproaches.Supported(kind)
}

// DefaultApproach reports codex's default (first-declared) approach for kind:
// Hook for context (its only approach — the existing cache-file write), the
// native file for settings/skills, absent for the folded MCP kind.
func (Surfaces) DefaultApproach(kind agent.SurfaceKind) (agent.Approach, bool) {
	return codexApproaches.Default(kind)
}

// SurfaceFor resolves one (kind, approach) to the concrete codex surface via the
// shared table lookup. context via Hook returns the SAME contextSurface
// Deliveries() would (the raw cache-file write the SessionStart hook reads; a
// no-op — nil handle — with no fragments); settings resolves to the folded
// config surface.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return codexApproaches.SurfaceFor("codex", map[agent.SurfaceKind]agent.Delivery{
		agent.SurfaceContext:  s.Context,
		agent.SurfaceSettings: s.Config,
		agent.SurfaceSkills:   s.Skills,
	}, kind, a)
}

// SharedRealization reports no realization for any kind: codex has no out-of-cwd
// redirect for ANY surface, so a SHARED-cwd delivery always falls back to the
// loud well-known write.
func (Surfaces) SharedRealization(agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts. Every codex surface is a KindedDelivery.
var (
	_ agent.KindedDelivery = (*contextSurface)(nil)
	_ agent.KindedDelivery = (*configSurface)(nil)
	_ agent.Delivered      = deliveredFunc(nil)
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
