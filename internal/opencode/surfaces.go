//go:build parked_engines

package opencode

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands opencode's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go) for the STATIC materialize path — each surface
// as an object implementing agent.Delivery, wrapping the OpencodeWriter verbatim.
//
// opencode folds everything into ONE strictly-validated opencode.json, so — like
// codex folds config.toml — opencode has just TWO surfaces:
//
//	surface  | opencode.json key / file                | also a SharedRealization?
//	---------|-----------------------------------------|--------------------------
//	context  | instructions[] -> .opencode/ctxloom-context.md | ❌ no out-of-cwd flag
//	settings | mcp{}                                     | ❌ no out-of-cwd flag
//	skills   | .opencode/skill/<name>/SKILL.md + skills.paths | ❌ no out-of-cwd flag
//
// MCP folds into the settings surface (SurfaceMCP is ABSENT from the approach
// table, exactly as codex does). opencode has no ctxloom-managed hook mechanism and
// no per-invocation config redirect, so every surface is a WELL-KNOWN write with no
// SharedRealization. read-only permission is NOT here: it is a per-RUN posture
// delivered transiently by the chat path (chat.go).
//
// NOTE (live vs static): the LIVE run/oneshot path does NOT use these surfaces — it
// writes opencode.json transiently in Chat and restores it afterward, so a live run
// leaves no debris. These surfaces serve the name-only `profile materialize` path,
// which WANTS a persistent opencode.json.

// contextSurface is opencode's context surface: the ctxloom-owned
// .opencode/ctxloom-context.md, referenced from opencode.json's `instructions` key,
// written via the reused ContextWriter core (OpencodeWriter.WriteContext).
type contextSurface struct {
	context string
	fs      afero.Fs
}

// Deliver writes the context file + instructions reference via WriteContext and
// returns a handle whose Cleanup removes them (writing empty context). This is the
// shared agent.DeliverManagedContext shape, the same one codex/kiro/claude use.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&OpencodeWriter{FS: s.fs}, dir, s.context)
}

// UnsafeInfo returns opencode's context identity for the DeliverShared fallback's warning.
func (s *contextSurface) UnsafeInfo() string { return "opencode/context" }

// State implements agent.StateReader: what .opencode/ctxloom-context.md
// currently carries. That file is ctxloom's OUTRIGHT — OpencodeWriter.WriteContext
// writes it whole and removes it whole — so it reads through
// agent.ReadOwnedContext, not the marker split ReadManagedContext performs for
// the engines whose context shares a file with hand-authored prose.
//
// It answers for the PAYLOAD file only, not for the `instructions` reference
// WriteContext also puts in opencode.json alongside it. That reference is the
// config surface's half of the same write, and a currency report for the
// context route that went missing whenever a user reordered their opencode.json
// would be reporting the wrong file's fault against this one's name.
func (s *contextSurface) State(dir string) (agent.DeliveryState, error) {
	fs := agent.GetFS(s.fs)
	w := &OpencodeWriter{FS: fs}
	state, err := agent.ReadOwnedContext(fs, w.contextFilePath(dir), opencodeContextFile)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// configSurface is opencode's folded settings + MCP surface: opencode.json's `mcp`
// key, written via the reused OpencodeWriter.WriteSettings.
type configSurface struct {
	bundleMCP map[string]wire.MCPServer
	fs        afero.Fs
}

// Deliver writes the managed MCP servers into opencode.json via the reused writer;
// Cleanup drops ONLY the ctxloom-managed servers (removeMCP), leaving the context
// surface's `instructions` in the same file untouched.
func (s *configSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &OpencodeWriter{FS: s.fs}
	if err := w.WriteSettings(nil, s.bundleMCP, dir); err != nil {
		return nil, err
	}
	return agent.DeliveredFunc(func() error { return w.removeMCP(dir) }), nil
}

// UnsafeInfo returns opencode's config identity for the DeliverShared fallback's warning.
func (s *configSurface) UnsafeInfo() string { return "opencode/config" }

// opencode's commands surface — the custom-command .md files under
// .opencode/command/ — is the shared agent.ManagedCommandsDelivery bound to
// opencode's manifest-scoped WriteCommandFiles (built in NewSurfaces); its
// write-then-revert-with-nil shape is identical across engines, so it lives in
// internal/shared/agent, not here. (On the LIVE chat/oneshot path opencode
// materializes these transiently in Chat, not via this surface — see chat.go.)
//
// opencode's skills surface — the Agent Skill packages under .opencode/skill/
// — is the shared agent.ManagedSkillPackagesDelivery bound to
// reconcileSkillsSurface (skillfiles.go), which ALSO reconciles the
// opencode.json `skills.paths` registration alongside the package tree (built
// in NewSurfaces). Like commands, the LIVE chat/oneshot path materializes
// skills transiently in Chat via the SAME reconcileSkillsSurface function, so
// the two paths can't diverge on what "materializing skills" means.

// Surfaces is opencode's set of delivery surfaces for one run — context
// (instructions) and config (mcp), both folded into opencode.json, plus commands
// (the custom-command dir) and skills (the Agent Skills dir + skills.paths). The
// commands/skills surfaces serve the persistent `profile materialize` path; a
// live run delivers both transiently in Chat.
type Surfaces struct {
	// TableDispatch carries opencode's declared approach table and the two
	// mechanical dispatch methods (SupportedApproaches / DefaultApproach) it
	// answers — see agent.TableDispatch. SurfaceFor stays below: it resolves a
	// concrete surface, which is this backend's own business.
	agent.TableDispatch

	Context  *contextSurface
	Config   *configSurface
	Commands *agent.ManagedCommandsDelivery
	Skills   *agent.ManagedSkillPackagesDelivery

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once here.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds opencode's surfaces from a run's shared inputs (the assembled
// context string, the merged MCP + bundle servers, the command exports, and the
// skill exports). A nil fs defaults to the OS filesystem. No surface is race-safe
// (opencode exposes no out-of-cwd flag).
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	ctx := &contextSurface{context: in.Context, fs: fs}
	config := &configSurface{bundleMCP: in.BundleMCP, fs: fs}
	commands := agent.NewManagedCommandsDelivery("opencode/commands", in.Commands, func(dir string, commands []agent.CommandExport) error {
		return WriteCommandFiles(dir, commands, agent.WithCommandFS(fs))
	})
	skills := agent.NewManagedSkillPackagesDelivery("opencode/skills", in.Skills, func(dir string, skills []agent.SkillExport) error {
		return reconcileSkillsSurface(fs, dir, skills)
	})
	return Surfaces{
		TableDispatch: agent.TableDispatch{Table: opencodeApproaches},
		Context:       ctx,
		Config:        config,
		Commands:      commands,
		Skills:        skills,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  ctx,
			agent.SurfaceSettings: config,
			agent.SurfaceCommands: commands,
			agent.SurfaceSkills:   skills,
		},
	}
}

// opencodeApproaches is opencode's DECLARED per-surface approach table: context,
// settings, and commands are each a single native file (opencode.json, plus the
// context file the instructions key points at, plus the .opencode/command/ dir).
// SurfaceMCP is deliberately ABSENT — it folds into the settings surface.
var opencodeApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachUnsafeFile},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceCommands: {agent.ApproachUnsafeFile},
	agent.SurfaceSkills:   {agent.ApproachUnsafeFile},
}

// SurfaceFor resolves one (kind, approach) to the concrete opencode surface via the
// shared table lookup against s.dispatch (built once in NewSurfaces).
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return opencodeApproaches.SurfaceFor("opencode", s.dispatch, kind, a)
}

// SharedRealization reports no realization for any (kind, approach) pair:
// opencode has no out-of-cwd redirect for ANY surface, so a SHARED-cwd
// delivery always falls back to the loud well-known write.
func (Surfaces) SharedRealization(agent.SurfaceKind, agent.Approach) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts.
var (
	_ agent.SurfaceSet = Surfaces{}
	// The ctxloom-owned context file also answers for what it delivered.
	_ agent.StateReader = (*contextSurface)(nil)
)
