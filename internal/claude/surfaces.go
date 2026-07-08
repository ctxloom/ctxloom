package claude

import (
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands claude's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go): each of claude's surfaces as an object that
// implements agent.Delivery (the engine's well-known write) and, where claude
// offers an out-of-cwd flag, ALSO agent.RaceSafeDelivery (an isolated per-run
// file a SharedCell can accept without a race). It is ADDITIVE: every surface
// object WRAPS an existing claude writer verbatim — appendFlagDelivery
// (contextdelivery.go), fileTemplateDelivery (surfacedelivery.go), and the
// ContextWriter core WriteContext (claude.go). Nothing here is wired into the
// launch path (launch_backend.go / Setup / buildArgs); the flag-consuming wiring
// (--append-system-prompt-file / --mcp-config / --settings) is Phase 2 (plan S4).
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, S1 spec):
//
//	surface   | Delivery (well-known)     | also RaceSafeDelivery? (out-of-cwd flag)
//	----------|---------------------------|-----------------------------------------
//	context   | CLAUDE.md                 | ✅ --append-system-prompt-file <file>
//	MCP       | .mcp.json                 | ✅ --mcp-config <file> (--strict-mcp-config = replace)
//	settings  | .claude/settings.json     | ✅ --settings <file> (carries hooks)
//	skills    | .claude/commands/         | ❌ no out-of-cwd flag → Unsafe or isolated cell
//
// claude folds "settings + hooks" into ONE surface because claude's hooks live
// inside .claude/settings.json — there is no separate hooks file to deliver.

// dirPlacement is a trivial agent.Placement whose Dir() returns a fixed
// directory. It adapts a plain dir into the Placement the reused writers
// (fileTemplateDelivery, appendFlagDelivery) construct against — for the
// well-known Delivery the dir arrives at call time; for the race-safe variants it
// is the per-run, out-of-cwd location the surface was built with.
type dirPlacement struct{ dir string }

// Dir returns the fixed directory this placement wraps.
func (p dirPlacement) Dir() string { return p.dir }

// contextSurface is claude's context surface.
//
// Delivery (well-known) writes CLAUDE.md into the target dir via claude's
// ContextWriter core (ClaudeCodeHookWriter.WriteContext) — the whole-file static
// surface an externally-launched session reads directly. Its cleanup removes that
// file, the honest reversal of a whole-file write.
//
// RaceSafeDelivery (isolated) writes the framed <hash>.sysprompt.md into the
// out-of-cwd placement via the existing appendFlagDelivery and exposes its path
// (Path) for claude's --append-system-prompt-file. Because that file lands
// outside the shared cwd, the context surface is race-safe and a SharedCell
// accepts it.
type contextSurface struct {
	context string
	fs      afero.Fs
	appendD *appendFlagDelivery // reused isolated append-flag writer (out-of-cwd)
}

// newContextSurface builds the context surface. isolated is the out-of-cwd
// placement the append-flag file lands in; fs must already be resolved.
func newContextSurface(context string, isolated agent.Placement, fs afero.Fs) *contextSurface {
	return &contextSurface{
		context: context,
		fs:      fs,
		appendD: newAppendFlagDelivery(isolated, fs),
	}
}

// Deliver writes CLAUDE.md into dir via the ContextWriter core and returns a
// handle whose Cleanup removes it.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	w := &ClaudeCodeHookWriter{FS: s.fs}
	if _, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: dir, Context: s.context}); err != nil {
		return nil, err
	}
	fs := s.fs
	path := filepath.Join(dir, "CLAUDE.md")
	return deliveredFunc(func() error { return fs.Remove(path) }), nil
}

// DeliverIsolated writes the framed context file through the reused
// appendFlagDelivery into the out-of-cwd placement; Path then exposes it.
func (s *contextSurface) DeliverIsolated() (agent.Delivered, error) {
	return s.appendD.DeliverContext(s.context)
}

// Path returns the framed <hash>.sysprompt.md written by DeliverIsolated (for
// --append-system-prompt-file), or "" before delivery / for empty context.
func (s *contextSurface) Path() string { return s.appendD.Path() }

// mcpSurface is claude's MCP surface.
//
// Delivery (well-known) writes .mcp.json into the target dir via the reused
// fileTemplateDelivery.DeliverMCP. RaceSafeDelivery writes the same merged
// .mcp.json into the out-of-cwd placement and exposes its path (Path) for
// --mcp-config <file> (paired with --strict-mcp-config, which replaces the
// project .mcp.json rather than merging — a buildArgs concern in Phase 2).
type mcpSurface struct {
	mcp      *wire.MCPConfig
	bundle   map[string]wire.MCPServer
	fs       afero.Fs
	isolated agent.Placement // out-of-cwd location for the --mcp-config file
	path     string          // set by DeliverIsolated: the out-of-cwd .mcp.json
}

// Deliver writes .mcp.json into dir via the reused file-template MCP writer.
func (s *mcpSurface) Deliver(dir string) (agent.Delivered, error) {
	return newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs).DeliverMCP(s.mcp, s.bundle)
}

// DeliverIsolated writes the merged .mcp.json into the out-of-cwd placement and
// records its path for --mcp-config.
func (s *mcpSurface) DeliverIsolated() (agent.Delivered, error) {
	dir := s.isolated.Dir()
	handle, err := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs).DeliverMCP(s.mcp, s.bundle)
	if err != nil {
		return nil, err
	}
	s.path = (&ClaudeCodeHookWriter{FS: s.fs}).MCPConfigPath(dir)
	return handle, nil
}

// Path returns the out-of-cwd .mcp.json written by DeliverIsolated (for
// --mcp-config <file>), or "" before delivery.
func (s *mcpSurface) Path() string { return s.path }

// settingsSurface is claude's settings surface (hooks + statusline; claude keeps
// them in a single .claude/settings.json).
//
// Delivery (well-known) writes .claude/settings.json into the target dir via the
// reused fileTemplateDelivery.DeliverSettings. RaceSafeDelivery writes the same
// settings JSON (including hooks) into the out-of-cwd placement and exposes its
// path (Path) for --settings <file>.
type settingsSurface struct {
	hooks            *wire.HooksConfig
	manageStatusline bool
	fs               afero.Fs
	isolated         agent.Placement // out-of-cwd location for the --settings file
	path             string          // set by DeliverIsolated: the out-of-cwd settings.json
}

// Deliver writes .claude/settings.json into dir via the reused file-template
// settings writer.
func (s *settingsSurface) Deliver(dir string) (agent.Delivered, error) {
	return newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs).DeliverSettings(s.hooks, s.manageStatusline)
}

// DeliverIsolated writes the settings JSON (incl. hooks) into the out-of-cwd
// placement and records its path for --settings.
func (s *settingsSurface) DeliverIsolated() (agent.Delivered, error) {
	dir := s.isolated.Dir()
	handle, err := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs).DeliverSettings(s.hooks, s.manageStatusline)
	if err != nil {
		return nil, err
	}
	s.path = (&ClaudeCodeHookWriter{FS: s.fs}).SettingsPath(dir)
	return handle, nil
}

// Path returns the out-of-cwd settings.json written by DeliverIsolated (for
// --settings <file>), or "" before delivery.
func (s *settingsSurface) Path() string { return s.path }

// skillsSurface is claude's skills surface: the slash-command exports under
// .claude/commands/. It implements Delivery ONLY — claude has no out-of-cwd flag
// for slash-commands, so it is deliberately NOT a RaceSafeDelivery. It reaches a
// SharedCell (the user's live cwd) only via the explicit, warned agent.Unsafe
// adapter (see UnsafeSkillsReason); first preference is always an isolated cell.
type skillsSurface struct {
	skills []agent.CommandExport
	fs     afero.Fs
}

// Deliver writes .claude/commands/ into dir via the reused file-template skills
// writer.
func (s *skillsSurface) Deliver(dir string) (agent.Delivered, error) {
	return newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs).DeliverSkills(s.skills)
}

// UnsafeSkillsReason is the sanctioned-unsafe reason for claude's skills surface:
// claude exposes no out-of-cwd flag for .claude/commands/ slash-commands, so
// landing them in a shared cwd is permitted only through the loud agent.Unsafe
// adapter. dir is the shared cwd the well-known write lands in. The stable
// UnsafeReason is what a gen-docs pass (plan S3) enumerates into the reference
// page of sanctioned unsafe deliveries.
func UnsafeSkillsReason(dir string) agent.UnsafeReason {
	return agent.UnsafeReason{
		Surface: "skills",
		Why:     "claude has no out-of-cwd flag for .claude/commands/ slash-commands",
		Dir:     dir,
		Class:   strictness.ClassApply,
	}
}

// SurfaceInputs carries the per-run data claude's surfaces write. It mirrors what
// the launch path already assembles — the context text, the merged MCP config +
// profile/builtin bundle servers, the hook set + statusline policy, and the skill
// exports. S1 only defines and fills it in tests; Phase 2 (S4) feeds it from Setup.
type SurfaceInputs struct {
	Context          string
	MCP              *wire.MCPConfig
	BundleMCP        map[string]wire.MCPServer
	Hooks            *wire.HooksConfig
	ManageStatusline bool
	Skills           []agent.CommandExport
}

// Surfaces is claude's set of delivery surfaces for one run, exposed so a caller
// can iterate them and hand each to a cell (Deliveries for an isolated cell,
// RaceSafeForSharedCwd for the shared live cwd). claude has four surface objects
// — context, MCP, settings (which carries hooks), and skills.
type Surfaces struct {
	Context  *contextSurface
	MCP      *mcpSurface
	Settings *settingsSurface
	Skills   *skillsSurface
}

// NewSurfaces builds claude's surfaces from a run's inputs. isolated is the
// per-run, OUT-OF-CWD placement the race-safe variants write into (the
// append-flag file, the --mcp-config file, the --settings file); a nil fs
// defaults to the OS filesystem. Every surface's well-known Delivery takes its
// target dir at call time, so only the race-safe variants bind isolated here.
func NewSurfaces(in SurfaceInputs, isolated agent.Placement, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	return Surfaces{
		Context:  newContextSurface(in.Context, isolated, fs),
		MCP:      &mcpSurface{mcp: in.MCP, bundle: in.BundleMCP, fs: fs, isolated: isolated},
		Settings: &settingsSurface{hooks: in.Hooks, manageStatusline: in.ManageStatusline, fs: fs, isolated: isolated},
		Skills:   &skillsSurface{skills: in.Skills, fs: fs},
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe.
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.MCP, s.Settings, s.Skills}
}

// RaceSafeForSharedCwd returns every surface prepared for a SharedCell (the
// user's live cwd) at dir: the three flag-backed surfaces as-is, and skills
// wrapped in the loud agent.Unsafe adapter (claude offers no out-of-cwd flag for
// slash-commands, so this is the sanctioned last resort — an isolated cell is
// always the first preference). Each returned value is a RaceSafeDelivery, so it
// is assignable to SharedCell.Deliver.
func (s Surfaces) RaceSafeForSharedCwd(dir string) []agent.RaceSafeDelivery {
	return []agent.RaceSafeDelivery{
		s.Context,
		s.MCP,
		s.Settings,
		agent.Unsafe(s.Skills, UnsafeSkillsReason(dir)),
	}
}

// Compile-time capability contracts. Context/MCP/settings are dual-capable
// (Delivery + RaceSafeDelivery via a flag); skills is Delivery-ONLY. That
// skillsSurface is deliberately NOT assignable to agent.RaceSafeDelivery is the
// compile-time guarantee it cannot enter a SharedCell except through Unsafe — a
// negative case asserted as a compile-note in surfaces_test.go.
var (
	_ agent.Delivery         = (*contextSurface)(nil)
	_ agent.RaceSafeDelivery = (*contextSurface)(nil)
	_ agent.Delivery         = (*mcpSurface)(nil)
	_ agent.RaceSafeDelivery = (*mcpSurface)(nil)
	_ agent.Delivery         = (*settingsSurface)(nil)
	_ agent.RaceSafeDelivery = (*settingsSurface)(nil)
	_ agent.Delivery         = (*skillsSurface)(nil)
	_ agent.Placement        = dirPlacement{}
)
