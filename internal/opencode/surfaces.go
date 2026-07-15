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
//
// MCP folds into the settings surface (SurfaceMCP is ABSENT from the approach
// table, exactly as codex does). opencode has no ctxloom-managed hook mechanism and
// no per-invocation config redirect, so every surface is a WELL-KNOWN write with no
// SharedRealization. Commands (command exports) and read-only permission are NOT
// here: permission is a per-RUN posture delivered transiently by the chat path
// (chat.go), and command exports are a later slice.
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

// Kind reports opencode's context surface (instructions -> .opencode/ctxloom-context.md).
func (s *contextSurface) Kind() agent.SurfaceKind { return agent.SurfaceContext }

// configSurface is opencode's folded settings + MCP surface: opencode.json's `mcp`
// key, written via the reused OpencodeWriter.WriteSettings.
type configSurface struct {
	mcp       *wire.MCPConfig
	bundleMCP map[string]wire.MCPServer
	fs        afero.Fs
}

// Deliver writes the managed MCP servers into opencode.json via the reused writer;
// Cleanup drops ONLY the ctxloom-managed servers (removeMCP), leaving the context
// surface's `instructions` in the same file untouched.
func (s *configSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &OpencodeWriter{FS: s.fs}
	if err := w.WriteSettings(nil, s.mcp, s.bundleMCP, dir); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error { return w.removeMCP(dir) }), nil
}

// UnsafeInfo returns opencode's config identity for the DeliverShared fallback's warning.
func (s *configSurface) UnsafeInfo() string { return "opencode/config" }

// Kind reports opencode's config surface as the settings surface — it folds
// opencode.json's `mcp` key in, so selecting either Settings or MCP gets it.
func (s *configSurface) Kind() agent.SurfaceKind { return agent.SurfaceSettings }

// deliveredFunc adapts a cleanup closure to agent.Delivered so a surface can return
// its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

// opencode's commands surface — the custom-command .md files under
// .opencode/command/ — is the shared agent.ManagedCommandsDelivery bound to
// opencode's manifest-scoped WriteCommandFiles (built in NewSurfaces); its
// write-then-revert-with-nil shape is identical across engines, so it lives in
// internal/shared/agent, not here. (On the LIVE chat/oneshot path opencode
// materializes these transiently in Chat, not via this surface — see chat.go.)

// Surfaces is opencode's set of delivery surfaces for one run — context
// (instructions) and config (mcp), both folded into opencode.json, plus commands
// (the custom-command dir). The commands surface serves the persistent
// `profile materialize` path; a live run delivers commands transiently in Chat.
type Surfaces struct {
	Context  *contextSurface
	Config   *configSurface
	Commands *agent.ManagedCommandsDelivery

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once here.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds opencode's surfaces from a run's shared inputs (the assembled
// context string, the merged MCP + bundle servers, and the command exports).
// A nil fs defaults to the OS filesystem. No surface is race-safe (opencode exposes
// no out-of-cwd flag).
func NewSurfaces(in agent.SurfaceInputs, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	ctx := &contextSurface{context: in.Context, fs: fs}
	config := &configSurface{mcp: in.MCP, bundleMCP: in.BundleMCP, fs: fs}
	commands := agent.NewManagedCommandsDelivery("opencode/commands", in.Commands, func(dir string, commands []agent.CommandExport) error {
		return WriteCommandFiles(dir, commands, agent.WithCommandFS(fs))
	})
	return Surfaces{
		Context:  ctx,
		Config:   config,
		Commands: commands,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  ctx,
			agent.SurfaceSettings: config,
			agent.SurfaceCommands: commands,
		},
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target).
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.Config, s.Commands}
}

// opencodeApproaches is opencode's DECLARED per-surface approach table: context,
// settings, and commands are each a single native file (opencode.json, plus the
// context file the instructions key points at, plus the .opencode/command/ dir).
// SurfaceMCP is deliberately ABSENT — it folds into the settings surface.
var opencodeApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachUnsafeFile},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceCommands: {agent.ApproachUnsafeFile},
}

// SupportedApproaches reports opencode's declared approach table for kind.
func (Surfaces) SupportedApproaches(kind agent.SurfaceKind) []agent.Approach {
	return opencodeApproaches.Supported(kind)
}

// DefaultApproach reports opencode's default (first-declared: the native file)
// approach for kind.
func (Surfaces) DefaultApproach(kind agent.SurfaceKind) (agent.Approach, bool) {
	return opencodeApproaches.Default(kind)
}

// SurfaceFor resolves one (kind, approach) to the concrete opencode surface via the
// shared table lookup against s.dispatch (built once in NewSurfaces).
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	return opencodeApproaches.SurfaceFor("opencode", s.dispatch, kind, a)
}

// SharedRealization reports no realization for any kind: opencode has no out-of-cwd
// redirect for ANY surface, so a SHARED-cwd delivery always falls back to the loud
// well-known write.
func (Surfaces) SharedRealization(agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	return nil, false
}

// Compile-time capability contracts.
var (
	_ agent.KindedDelivery = (*contextSurface)(nil)
	_ agent.KindedDelivery = (*configSurface)(nil)
	_ agent.Delivered      = deliveredFunc(nil)
	_ agent.SurfaceSet     = Surfaces{}
)
