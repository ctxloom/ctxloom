package kiro

import (
	"fmt"
	"strings"

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
// the managed-command writer (WriteCommandFiles → .kiro/skills/), and the
// managed-skill-package writer (WriteSkillFiles → .kiro/skills/, skillfiles.go).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, "Per-engine
// race-safe coverage"): kiro's context, MCP, commands, and skills surfaces are
// WELL-KNOWN-ONLY (plain agent.Delivery). kiro DOES have a per-agent lever — the
// settings + hooks live in .kiro/agents/<name>.json, selected at launch by
// `--agent <name>` — but that is NAME-based isolation resolved in buildArgs, a
// launch concern orthogonal to this seam; at the delivery layer every kiro
// surface is still a well-known write into a project-rooted file, so ALL FIVE are
// modeled as plain agent.Delivery here (none has a SharedRealization).
//
//	surface  | well-known target                       | also a SharedRealization?
//	---------|-----------------------------------------|-----------------------
//	context  | .kiro/steering/ctxloom-context.md       | ❌ (steering, auto-loaded)
//	MCP      | .kiro/settings/mcp.json                 | ❌ no flag
//	settings | .kiro/agents/<name>.json (hooks)        | ❌ (--agent is orthogonal)
//	commands | .kiro/skills/<name>/SKILL.md            | ❌ no flag
//	skills   | .kiro/skills/<name>/SKILL.md (+ files)  | ❌ no flag
//
// kiro reads steering rather than firing a SessionStart hook for context (the
// context surface is a whole-file steering write, not a hook), and its hooks live
// inside the agent JSON (settings + hooks fold into one surface, as claude folds
// settings + hooks into settings.json).
//
// commands and skills share ONE native directory — kiro's only mechanism is
// agentskills SKILL.md, auto-discovered via the `skill://.kiro/skills/**/
// SKILL.md` glob (kiro/settings.go), invocable both as a model-selected skill
// and as a /<name> slash command. When a command and a skill share a name,
// the shared agent.FilterCommandsClaimedBySkills (invoked from NewSurfaces via
// agent.NewSkillShapedCommandsAndSkills) resolves the D6
// collision as skill-wins BEFORE the commands delivery is even built, so the
// two writers' manifests (kiroManifest / kiroSkillManifest) never both claim
// the same path.

// contextSurface is kiro's context surface: the ctxloom-owned steering file
// (.kiro/steering/ctxloom-context.md), written via the reused ContextWriter core
// (WriteContext). Delivery-ONLY.
type contextSurface struct {
	context string
	fs      afero.Fs
}

// Deliver writes the steering file via WriteContext and returns a handle whose
// Cleanup removes it (writing empty context). This is the shared
// agent.DeliverManagedContext shape, the same one
// claude's CLAUDE.md and codex's AGENTS.md context surfaces use.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&KiroWriter{FS: s.fs}, dir, s.context)
}

// UnsafeInfo returns kiro's context identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *contextSurface) UnsafeInfo() string { return "kiro/context" }

// State implements agent.StateReader: what .kiro/steering/ctxloom-context.md
// currently carries. The steering file is ctxloom's OUTRIGHT — kiro has no
// hand-authored half of it, and writeSteering rewrites it whole — so it reads
// through agent.ReadOwnedContext (the whole payload) rather than
// ReadManagedContext (a marker section), and strips the front matter
// writeSteering framed the context with so the comparison is context against
// context.
func (s *contextSurface) State(dir string) (agent.DeliveryState, error) {
	fs := agent.GetFS(s.fs)
	w := &KiroWriter{FS: fs}
	state, err := agent.ReadOwnedContext(fs, w.steeringPath(dir), steeringRel())
	if err != nil {
		return nil, err
	}
	state.Content = strings.TrimPrefix(state.Content, steeringFrontMatter)
	return state, nil
}

// mcpSurface is kiro's MCP surface: .kiro/settings/mcp.json, written via the
// shared MCP-file reconciler (mcpFile().WriteServers). Delivery-ONLY.
type mcpSurface struct {
	bundleMCP       map[string]wire.MCPServer
	fs              afero.Fs
	commandOverride string // see agent.SurfaceInputs.MCPCommandOverride
}

// Deliver writes .kiro/settings/mcp.json via the reused reconciler and returns a
// handle whose Cleanup drops the ctxloom-managed servers (RemoveServers). Reads
// s.commandOverride — settingsSurface below and opencode's configSurface are
// deliberately NOT touched the same way: the override is MCP-only (it
// replaces the ctxloom stdio command), and neither the agent JSON nor
// opencode's config carries that command.
// reprise:accept-drift
func (s *mcpSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &KiroWriter{FS: s.fs, mcpCommandOverride: s.commandOverride}
	if err := w.mcpFile(dir).WriteServers(s.bundleMCP); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error { return w.mcpFile(dir).RemoveServers() }), nil
}

// UnsafeInfo returns kiro's MCP identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *mcpSurface) UnsafeInfo() string { return "kiro/mcp" }

// settingsSurface is kiro's folded settings + hooks surface: the ctxloom-owned
// custom-agent config .kiro/agents/<name>.json, written via the reused mapHooks +
// writeAgentConfig. kiro's hooks live inside this agent JSON, so settings + hooks
// are one surface. Delivery-ONLY.
//
// agentName (closing the Phase 2 note this used to carry): the agent's NAME
// is what `--agent <name>` selects at launch — kiro's per-agent
// isolation lever. It must be the SAME name buildArgs launches with, so it
// rides in.AgentName from NewSurfaces (empty uses defaultAgentName, unchanged
// from before this field existed).
type settingsSurface struct {
	hooks     *wire.HooksConfig
	fs        afero.Fs
	agentName string
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
	w := &KiroWriter{FS: s.fs, agentName: s.agentName}
	_, agentHooks := w.mapHooks(hooks.Unified, hooks.Plugins["kiro"])
	if err := w.writeAgentConfig(dir, agentHooks); err != nil {
		return nil, err
	}
	fs := s.fs
	path := w.agentPath(dir)
	return agent.DeliveredFunc(func() error {
		exists, err := afero.Exists(fs, path)
		if err != nil {
			return fmt.Errorf("failed to check %s: %w", path, err)
		}
		if !exists {
			return nil
		}
		return fs.Remove(path)
	}), nil
}

// UnsafeInfo returns kiro's settings identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *settingsSurface) UnsafeInfo() string { return "kiro/settings" }

// kiro's commands surface — the agentskills SKILL.md files under .kiro/skills/ —
// is the shared agent.ManagedCommandsDelivery bound to kiro's manifest-scoped
// WriteCommandFiles (built in NewSurfaces); its write-then-revert-with-nil shape
// is identical across engines, so it lives in internal/shared/agent, not here.

// kiro's D6 collision resolution (skill-command-split plan §3.3/§3.5 B5): kiro
// has ONE native directory, .kiro/skills/<name>/, that serves BOTH the renamed
// command surface (a slash-command rendered as SKILL.md) and the true Agent
// Skills surface. When a command and a skill share the same name, both would
// target the identical path .kiro/skills/<name>/SKILL.md — resolved as
// SKILL-WINS by the shared agent.FilterCommandsClaimedBySkills (called from
// NewSurfaces below, via agent.NewSkillShapedCommandsAndSkills): the richer
// native package is materialized and the command's rendering of that name is
// dropped before it ever reaches WriteCommandFiles.
//
// This is arbitrated at export-assembly time in NewSurfaces, rather than
// inside either writer, because that is the one place that keeps the TWO
// MANIFESTS (kiroManifest for commands, kiroSkillManifest for skills)
// coherent: a claimed name is simply never handed to the commands writer, so
// it never appears in kiroManifest and the skills writer is the SOLE owner of
// that path in kiroSkillManifest. No manifest ever claims a file the other
// wrote, so a later re-materialize (with the skill removed, or disabled) lets
// the command reclaim the name cleanly, and neither writer's cleanup can ever
// delete the other's live file. Only skills ENABLED for kiro claim a name —
// a skill disabled for this engine writes nothing here, so it must not shadow
// the command with the same name.

// Surfaces is kiro's set of delivery surfaces for one run — five surface
// objects: context (steering), MCP (settings/mcp.json), settings (agent JSON +
// hooks), commands, and skills (both of the latter two share .kiro/skills/,
// reconciled skill-wins by the shared
// agent.FilterCommandsClaimedBySkills, see the collision-resolution doc
// comment above NewSurfaces).
type Surfaces struct {
	// TableDispatch carries kiro's declared approach table and the two
	// mechanical dispatch methods (SupportedApproaches / DefaultApproach) it
	// answers — see agent.TableDispatch. SurfaceFor stays below: it resolves a
	// concrete surface, which is this backend's own business.
	agent.TableDispatch

	Context  *contextSurface
	MCP      *mcpSurface
	Settings *settingsSurface
	Commands *agent.ManagedCommandsDelivery
	Skills   *agent.ManagedSkillPackagesDelivery

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once
	// here (not reallocated per SurfaceFor call) since it never changes after
	// construction.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds kiro's surfaces from a run's shared inputs (kiro uses the
// assembled context text, merged MCP + bundle servers, the hook set, the
// command exports, and the skill package exports). A nil fs defaults to the OS
// filesystem. Every kiro surface's Delivery takes its target dir at call time;
// none is modeled as race-safe here (the `--agent` lever is Phase 2).
//
// Commands are filtered through the shared agent.FilterCommandsClaimedBySkills
// BEFORE the commands delivery is built (via
// agent.NewSkillShapedCommandsAndSkills), so the skill-wins collision
// resolution (D6) is baked in here, once, rather than duplicated at every call
// site — kiro is currently the only caller of this shared constructor (a
// prior antigravity caller converged on the identical SKILL.md shape via the
// G3 fix before the engine was removed in 0.7.0), kept shared rather than
// inlined so the next engine with this same skill/command collision reuses
// it instead of a third copy.
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	context := &contextSurface{context: in.Context, fs: fs}
	mcp := &mcpSurface{bundleMCP: in.BundleMCP, fs: fs, commandOverride: in.MCPCommandOverride}
	settings := &settingsSurface{hooks: in.Hooks, fs: fs, agentName: in.AgentName}
	commands, skills := agent.NewSkillShapedCommandsAndSkills("kiro", "kiro", kiroSkillsDir, in, fs,
		func(dir string, cmds []agent.CommandExport, fs afero.Fs) error {
			return WriteCommandFiles(dir, cmds, agent.WithCommandFS(fs))
		},
		func(dir string, skills []agent.SkillExport, fs afero.Fs) error {
			return WriteSkillFiles(dir, skills, agent.WithCommandFS(fs))
		},
	)
	return Surfaces{
		TableDispatch: agent.TableDispatch{Table: kiroApproaches},
		Context:       context,
		MCP:           mcp,
		Settings:      settings,
		Commands:      commands,
		Skills:        skills,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  context,
			agent.SurfaceMCP:      mcp,
			agent.SurfaceSettings: settings,
			agent.SurfaceCommands: commands,
			agent.SurfaceSkills:   skills,
		},
	}
}

// kiroApproaches is kiro's DECLARED per-surface approach table (v2
// per-provider dispatch): every surface is a single native file. kiro reads
// .kiro/steering for context (no hook, no out-of-cwd flag), and its hooks live
// inside the agent JSON (settings + hooks fold into one surface). The mechanical
// lookups ride agent.ApproachTable; only this table is kiro's.
var kiroApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachUnsafeFile},
	agent.SurfaceMCP:      {agent.ApproachUnsafeFile},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceCommands: {agent.ApproachUnsafeFile},
	agent.SurfaceSkills:   {agent.ApproachUnsafeFile},
}

// SurfaceFor resolves one (kind, UnsafeFile) to the concrete kiro surface via
// the shared table lookup against s.dispatch (built once in NewSurfaces, not
// reallocated per call). kiro's agent JSON IS its settings surface, hooks
// folded in. Any other approach is unsupported — WithContext(Hook) on kiro
// fails loudly, matching kiro's steering (not hook) context.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return kiroApproaches.SurfaceFor("kiro", s.dispatch, kind, a)
}

// SharedRealization reports no realization for any (kind, approach) pair:
// kiro has no out-of-cwd redirect for ANY surface, so a SHARED-cwd delivery
// always falls back to the loud well-known write. (The `--agent` name lever
// isolates CONCURRENT agents at launch, orthogonally to this seam.)
func (Surfaces) SharedRealization(agent.SurfaceKind, agent.Approach) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts. Every kiro surface is a KindedDelivery.
var (
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
	// The steering-file context route also answers for what it delivered.
	_ agent.StateReader = (*contextSurface)(nil)
)
