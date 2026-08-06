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
//	surface   | well-known target                       | also a SharedRealization?
//	----------|------------------------------------------|-----------------------
//	context   | .ctxloom/cache/context/<hash>.md (hook) | ❌ no flag
//	agentsMD  | AGENTS.md (native, managed markers)     | ❌ no flag
//	config    | .codex/config.toml (hooks + MCP)        | ❌ no flag
//	commands  | $CODEX_HOME/prompts/<name>.md           | ❌ no flag
//	skills    | $CODEX_HOME/skills/<name>/SKILL.md      | ❌ no flag
//
// Three codex realities shape the decomposition:
//
//  1. config.toml is FOLDED. codex has a single atomic writer (WriteSettings)
//     that owns the [hooks] AND [mcp_servers] tables of one file. Splitting MCP
//     from hooks would mean two partial load-modify-save passes over the same
//     file — so, exactly as claude folds "settings + hooks" into one surface,
//     codex folds "settings + hooks + MCP" into one config surface (its
//     SupportedApproaches reports SurfaceMCP absent, keying the fold at the
//     SurfaceSelection builder level too).
//
//  2. context has TWO coexisting SURFACE OBJECTS, not one (taskloom lanky-plop
//     / tiny-ooze). contextSurface is the original hook route: the raw
//     fragments-keyed cache file (agent.WriteContextFile) a SessionStart hook
//     in config.toml reads — necessary for the RUN/LAUNCH path, which needs a
//     per-invocation content HASH delivered out-of-band of any workspace-fixed
//     file (see BaseContextProvider.Provide / setupViaCells). agentsMDSurface is
//     the new native route: codex reads a workspace-fixed AGENTS.md NATIVELY at
//     session start — no hook needed — via CodexHookWriter.WriteContext
//     (agent.ContextWriter), which merges into managed-section markers
//     (agent.WriteManagedContext), preserving hand-authored content outside
//     them byte-for-byte, exactly like claude's CLAUDE.md and antigravity's
//     AGENTS.md. It is keyed on the assembled context STRING
//     (SurfaceInputs.Context), because the STATIC materialize/init path only
//     ever has that string, never resolved Fragment objects — so before
//     agentsMDSurface existed, materialize's codex output silently carried NO
//     context (contextSurface saw an empty fragment slice and no-op'd).
//     Deliveries() delivers both; SupportedApproaches still reports context as
//     Hook-ONLY (see codexApproaches below) — agentsMDSurface has no
//     separately-selectable approach, it always rides alongside whatever
//     approach-dispatch selects for contextSurface.
//
//  3. commands are GLOBAL. codex discovers prompts only from $CODEX_HOME/prompts
//     (default ~/.codex/prompts) — NOT cwd-relative — so an isolated *directory*
//     would not isolate them. The commands surface therefore writes prompts under a
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
func cellScopedCodexHome(dir string) string { return filepath.Join(dir, ConfigDirName) }

// cellScopedPromptsDir is the prompts directory under the cell-scoped
// $CODEX_HOME — the isolated stand-in for the global codexPromptsDir().
func cellScopedPromptsDir(dir string) string {
	return filepath.Join(cellScopedCodexHome(dir), PromptsDirName)
}

// cellScopedSkillsDir is the Agent Skills directory under the cell-scoped
// $CODEX_HOME — the isolated stand-in for the global codexSkillsDir().
func cellScopedSkillsDir(dir string) string {
	return filepath.Join(cellScopedCodexHome(dir), SkillsDirName)
}

// contextSurface is codex's context surface. codex has no ContextWriter for
// this route: the context reaches the model as a raw context file
// (agent.WriteContextFile) that a SessionStart hook in config.toml reads. This
// is the RUN/LAUNCH path's route — it needs a per-invocation content HASH
// delivered out-of-band of any workspace-fixed file (see
// BaseContextProvider.Provide / setupViaCells), which a native AGENTS.md write
// cannot replace. Deliver writes that file into dir's well-known context
// cache; the SessionStart hook that consumes it is delivered by the config
// surface (it is one of the hooks). Delivery-ONLY — codex has no out-of-cwd
// context flag.
//
// This is one of TWO codex context routes that now coexist (see agentsMDSurface
// below for the other, native one); Surfaces.Deliveries() delivers both.
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
	return agent.DeliveredFunc(func() error { return fs.Remove(path) }), nil
}

// UnsafeInfo returns codex's context identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *contextSurface) UnsafeInfo() string { return "codex/context" }

// agentsMDSurface is codex's OTHER, native context route (taskloom
// lanky-plop/tiny-ooze): codex reads a workspace-fixed AGENTS.md NATIVELY at
// session start — no hook required — and CodexHookWriter now implements
// agent.ContextWriter to write it with managed-section markers
// (agent.WriteManagedContext), preserving hand-authored content outside them
// byte-for-byte, exactly like claude's CLAUDE.md and antigravity's AGENTS.md.
// This route is keyed on the assembled context STRING (SurfaceInputs.Context),
// because the STATIC materialize/init path only ever has that string, never
// resolved Fragment objects (AssembleContext returns a flattened string) — so
// before this surface existed, materialize's codex output silently carried NO
// context at all: contextSurface (above) is keyed on fragments, which
// materialize never populates, so it saw an empty slice and no-op'd.
// Delivery-ONLY, like every codex surface.
type agentsMDSurface struct {
	context string
	fs      afero.Fs
}

// Deliver merges the context into AGENTS.md and returns a handle whose Cleanup
// strips the managed section (removing the file when nothing user-authored
// remains) by writing empty context. This is the shared
// agent.DeliverManagedContext shape — the SAME one antigravity's and claude's
// own native-file ContextWriter surfaces use — not contextSurface's hash-file
// precision (where empty content genuinely creates nothing, so IT reports a
// nil handle): WriteContext("") on an absent file is a harmless no-op report
// (Removed with nothing to remove), not a call worth special-casing.
func (s *agentsMDSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&CodexHookWriter{FS: s.fs}, dir, s.context)
}

// UnsafeInfo returns codex's AGENTS.md identity for the DeliverShared
// fallback's warning (ResolvedSelection.deliverOneShared's unsafeNamed check,
// cells.go).
func (s *agentsMDSurface) UnsafeInfo() string { return "codex/agents-md" }

// configSurface is codex's folded settings + hooks + MCP surface: the single
// .codex/config.toml written by CodexHookWriter.WriteSettings, which owns the
// [hooks] and [mcp_servers] tables together. Delivery-ONLY — codex has no
// per-invocation config redirect.
type configSurface struct {
	hooks     *wire.HooksConfig
	mcp       *wire.MCPConfig
	bundleMCP map[string]wire.MCPServer
	fs        afero.Fs
	// homeOverride, when non-empty, is the project-dir-equivalent config.toml
	// (and, via the sibling commands/skills surfaces, prompts/skills) is
	// written under INSTEAD of the dir Deliver receives — the isolation-
	// provided CODEX_HOME's virtual project dir (white-dawn §2.2A; see
	// backend.go's resolveCodexProjectDir). Empty for the static
	// materialize/apply path (registry.go's NewSurfaces caller has no
	// isolation context), which keeps deriving from Deliver's dir exactly as
	// before this field existed.
	homeOverride string
	// trustAbsPath, when non-empty, pre-seeds `[projects."<trustAbsPath>"]
	// trust_level = "trusted"` into the written config.toml (white-dawn
	// §2.2A's trust pre-seed) — set ONLY alongside homeOverride, and only for
	// the two EPHEMERAL, never-committed homes (isolation-provided CODEX_HOME,
	// container-fresh $HOME) that are safe to auto-trust into (see
	// WriteSettingsWithTrust's doc).
	trustAbsPath string
	// mcpCommandOverride mirrors agent.SurfaceInputs.MCPCommandOverride: when
	// non-empty, replaces agent.CtxloomCommand() as the ctxloom-managed
	// [mcp_servers] entry's command (dire-five's fix; set only for an
	// isolated-container cell).
	mcpCommandOverride string
}

// Deliver writes .codex/config.toml into dir (or homeOverride, when set) via
// the reused WriteSettingsWithTrust and returns a handle whose Cleanup
// reverts the ctxloom-managed hooks and servers via RemoveSettings (user keys
// preserved). The homeOverride indirection is codex-specific (white-dawn
// §2.2A's single-owner CODEX_HOME fix) — the sibling backends' Deliver
// methods have no analogous split-target need, so this is a deliberate,
// reviewed divergence from their shape, not a missed update.
// reprise:accept-drift
func (s *configSurface) Deliver(dir string) (agent.Delivered, error) {
	target := dir
	if s.homeOverride != "" {
		target = s.homeOverride
	}
	w := &CodexHookWriter{FS: s.fs, MCPCommandOverride: s.mcpCommandOverride}
	if err := w.WriteSettingsWithTrust(s.hooks, s.mcp, s.bundleMCP, target, s.trustAbsPath); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error { return w.RemoveSettings(target) }), nil
}

// UnsafeInfo returns codex's config identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *configSurface) UnsafeInfo() string { return "codex/config" }

// codex's prompts surface is the shared agent.ManagedCommandsDelivery bound to
// codex's writer (built in NewSurfaces). codex prompts are GLOBAL
// ($CODEX_HOME/prompts), so the bound writer targets a CELL-SCOPED $CODEX_HOME
// derived from the delivery dir (cellScopedPromptsDir) via the shared
// manifest-scoped writer + codexPromptFile mapper — that cell-scoping is what
// lets a DirectoryIsolatedCell isolate them. The write-then-revert-with-nil shape
// is identical across engines, so it lives in internal/shared/agent, not here.
//
// codex's skills surface is the shared agent.ManagedSkillPackagesDelivery bound
// to codex's WriteSkillFiles (skillfiles.go), targeting the SAME cell-scoped
// $CODEX_HOME (cellScopedSkillsDir) as commands — codex skills are global
// ($CODEX_HOME/skills), exactly like its prompts.

// Surfaces is codex's set of delivery surfaces for one run. codex has five
// surface objects — context (the raw context file, for the SessionStart hook),
// agentsMD (the native AGENTS.md managed section — the other context route),
// config (config.toml's folded hooks + MCP), commands (cell-scoped prompts),
// and skills (cell-scoped Agent Skills packages).
type Surfaces struct {
	// TableDispatch carries codex's declared approach table and the two
	// mechanical dispatch methods (SupportedApproaches / DefaultApproach) it
	// answers — see agent.TableDispatch. SurfaceFor stays below: it resolves a
	// concrete surface, which is this backend's own business.
	agent.TableDispatch

	Context  *contextSurface
	AgentsMD *agentsMDSurface
	Config   *configSurface
	Commands *agent.ManagedCommandsDelivery
	Skills   *agent.ManagedSkillPackagesDelivery

	// routes composes Context+AgentsMD into the single Delivery SurfaceFor and
	// Deliveries() both expose for agent.SurfaceContext (agent.ComposedDelivery)
	// — built once here so every reader (approach-dispatch, isolated-cell
	// iteration) resolves against the SAME underlying instance, per
	// SurfaceSet.SurfaceFor's contract in cells.go.
	routes agent.ComposedDelivery

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once
	// here (not per SurfaceFor call) since it never changes after
	// construction — codex's SurfaceContext entry is s.routes (the composed
	// value), the one asymmetry in this cross-backend family: every other
	// backend's SurfaceFor builds an equivalent map inline because they have
	// no analogous precomputed composite to reference.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds codex's surfaces from a run's shared inputs. codex's
// hook-driven context route takes the Fragments; its native AGENTS.md route
// takes the assembled Context string (the STATIC materialize/init path only
// ever has the string — see agentsMDSurface's doc comment); it also uses the
// merged MCP + bundle servers, the hook set, and the command exports. A nil fs
// defaults to the OS filesystem. Every codex surface's Delivery takes its
// target dir at call time; none is race-safe (codex exposes no out-of-cwd
// flag), so there is no isolated placement to bind — EXCEPT the config/
// commands/skills surfaces' homeOverride: when non-empty (the live
// run/launch path, wired from Codex.buildSurfaces — see backend.go's
// resolveCodexProjectDir), they write under it INSTEAD of whatever dir a
// later Deliver call receives, making the isolation-provided CODEX_HOME the
// single owner (white-dawn §2.2A). Empty (the static materialize/apply path,
// via registry.go's backends.BuildSurfaces) keeps every surface deriving
// from Deliver's dir, unchanged from before homeOverride existed.
// trustAbsPath, when non-empty, pre-seeds config.toml's project trust (see
// WriteSettingsWithTrust) — passed through to the config surface only; it is
// meaningless without homeOverride (the in-tree path never sets it).
func NewSurfaces(in agent.SurfaceInputs, homeOverride, trustAbsPath string, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	ctxSurf := &contextSurface{fragments: in.Fragments, fs: fs}
	mdSurf := &agentsMDSurface{context: in.Context, fs: fs}
	config := &configSurface{hooks: in.Hooks, mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs, homeOverride: homeOverride, trustAbsPath: trustAbsPath, mcpCommandOverride: in.MCPCommandOverride}
	commands := agent.NewManagedCommandsDelivery("codex/commands (global $CODEX_HOME)", in.Commands, func(dir string, commands []agent.CommandExport) error {
		target := dir
		if homeOverride != "" {
			target = homeOverride
		}
		return agent.WriteManagedCommandFiles(fs, cellScopedPromptsDir(target), commands, codexPromptFile)
	})
	skills := agent.NewManagedSkillPackagesDelivery("codex/skills (global $CODEX_HOME)", in.Skills, func(dir string, skills []agent.SkillExport) error {
		target := dir
		if homeOverride != "" {
			target = homeOverride
		}
		return writeCodexSkillPackages(fs, cellScopedSkillsDir(target), skills)
	})
	routes := agent.ComposedDelivery{
		Parts:       []agent.Delivery{ctxSurf, mdSurf},
		Info:        "codex/context",
		SurfaceKind: agent.SurfaceContext,
	}
	return Surfaces{
		TableDispatch: agent.TableDispatch{Table: codexApproaches},
		Context:       ctxSurf,
		AgentsMD:      mdSurf,
		Config:        config,
		Commands:      commands,
		Skills:        skills,
		routes:        routes,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  routes,
			agent.SurfaceSettings: config,
			agent.SurfaceCommands: commands,
			agent.SurfaceSkills:   skills,
		},
	}
}

// codexApproaches is codex's DECLARED per-surface approach table (vital-tiger v2
// per-provider dispatch). context remains declared Hook-ONLY: the SessionStart
// inject-context hook's raw cache file is the only SEPARATELY-SELECTABLE
// context approach a caller can name. But `profile materialize` and the live
// run/launch path both resolve surfaces through
// agent.Select(set).WithEverything().Build(), which calls SurfaceFor EXACTLY
// ONCE per selected kind (cells.go's SurfaceSelection.Build) — so the Hook
// approach must resolve to something that ALSO performs the native AGENTS.md
// write, or that write would never reach either path. The routes field
// (an agent.ComposedDelivery) is that composition: naming UnsafeFile for codex's context
// remains an unsupported combo the builder rejects — there is no way to select
// "only the native file" separately through this table, the way
// claude/antigravity expose UnsafeFile as a first-class choice; codex's native
// write always rides alongside Hook.
// settings/commands are native-file-only. SurfaceMCP is deliberately ABSENT: MCP
// folds into the config/settings surface, so codex advertises no distinct MCP
// surface — selecting MCP is a permitted no-op, resolved by whichever selection
// also names Settings. The mechanical lookups ride agent.ApproachTable; only
// this table is codex's.
var codexApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachHook},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceCommands: {agent.ApproachUnsafeFile},
	agent.SurfaceSkills:   {agent.ApproachUnsafeFile},
}

// SurfaceFor resolves one (kind, approach) to the concrete codex surface via
// the shared table lookup against s.dispatch (built once in NewSurfaces, not
// reallocated per call). context via Hook resolves to s.routes, the composed
// Delivery that performs BOTH context routes (the raw cache-file write the
// SessionStart hook reads, AND the native AGENTS.md managed-marker write) —
// settings resolves to the folded config surface.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return codexApproaches.SurfaceFor("codex", s.dispatch, kind, a)
}

// SharedRealization reports no realization for any kind: codex has no out-of-cwd
// redirect for ANY surface, so a SHARED-cwd delivery always falls back to the
// loud well-known write.
func (Surfaces) SharedRealization(agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts. Every codex surface is a KindedDelivery.
var (
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
