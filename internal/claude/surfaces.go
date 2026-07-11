package claude

import (
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file lands claude's package on the unified surface-delivery seam
// (internal/shared/agent/cells.go): each of claude's surfaces as an object that
// implements agent.Delivery (the engine's well-known write) and, for context/MCP/
// settings, ALSO an isolated per-run DeliverIsolated an out-of-cwd launch flag
// consumes. Every surface object WRAPS an existing claude writer verbatim —
// appendFlagDelivery (contextdelivery.go), fileTemplateDelivery
// (surfacedelivery.go), and the ContextWriter core WriteContext (claude.go). The
// approach-dispatch methods below (SupportedApproaches / DefaultApproach /
// SurfaceFor / SharedRealization — vital-tiger v2) are the per-provider table the
// SurfaceSelection builder resolves a caller's named approach through; buildArgs
// (claudecode.go) reads each out-of-cwd file's Path() after a SHARED-cwd delivery
// converted via SharedRealization.
//
// Capability recap (VERIFIED — delivery-factory-unification.plan.md, S1 spec):
//
//	surface   | Delivery (well-known)     | also an out-of-cwd flag realization?
//	----------|---------------------------|-----------------------------------------
//	context   | CLAUDE.md                 | ✅ --append-system-prompt-file <file>
//	MCP       | .mcp.json                 | ✅ --mcp-config <file> (--strict-mcp-config = replace)
//	settings  | .claude/settings.json     | ✅ --settings <file> (carries hooks)
//	skills    | .claude/commands/         | ❌ no out-of-cwd flag → loud native write when shared
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
// DeliverIsolated writes the framed <hash>.sysprompt.md into the out-of-cwd
// placement via the existing appendFlagDelivery and exposes its path (Path) for
// claude's --append-system-prompt-file. Because that file lands outside the
// shared cwd, SharedRealization uses it for a race-free SHARED-cwd delivery.
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

// Kind reports claude's context surface (CLAUDE.md).
func (s *contextSurface) Kind() agent.SurfaceKind { return agent.SurfaceContext }

// mcpSurface is claude's MCP surface.
//
// Delivery (well-known) writes .mcp.json into the target dir via the reused
// fileTemplateDelivery.DeliverMCP. DeliverIsolated writes the same merged
// .mcp.json into the out-of-cwd placement and exposes its path (Path) for
// --mcp-config <file> (paired with --strict-mcp-config, which replaces the
// project .mcp.json rather than merging — a buildArgs concern).
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

// Kind reports claude's MCP surface (.mcp.json).
func (s *mcpSurface) Kind() agent.SurfaceKind { return agent.SurfaceMCP }

// settingsSurface is claude's settings surface (hooks + statusline; claude keeps
// them in a single .claude/settings.json).
//
// Delivery (well-known) writes .claude/settings.json into the target dir via the
// reused fileTemplateDelivery.DeliverSettings. DeliverIsolated writes the same
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

// Kind reports claude's settings surface (.claude/settings.json — hooks + statusline).
func (s *settingsSurface) Kind() agent.SurfaceKind { return agent.SurfaceSettings }

// skillsSurface is claude's skills surface: the slash-command exports under
// .claude/commands/. claude has no out-of-cwd flag for slash-commands and no
// SharedRealization (see Surfaces.SharedRealization below), so a SHARED-cwd
// delivery of it falls back to the loud well-known write; first preference is
// always an isolated cell. It self-describes via UnsafeInfo for that fallback's
// warning. (Unlike the other engines, claude's skills ride
// fileTemplateDelivery.DeliverSkills, which owns its own cleanup, so they are NOT
// the shared agent.ManagedSkillsDelivery.)
type skillsSurface struct {
	skills              []agent.CommandExport
	fs                  afero.Fs
	selfContainedSkills bool // mirrors SurfaceInputs.SelfContainedSkills; see DeliverSkills
}

// Deliver writes .claude/commands/ into dir via the reused file-template skills
// writer. selfContainedSkills rides along so a materialize target (a portable,
// self-contained tree) skips deduping against the delivering machine's
// ~/.claude/commands — see fileTemplateDelivery.DeliverSkills.
func (s *skillsSurface) Deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs)
	d.selfContainedSkills = s.selfContainedSkills
	return d.DeliverSkills(s.skills)
}

// UnsafeInfo returns claude's skills identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *skillsSurface) UnsafeInfo() string { return "claude/skills" }

// Kind reports claude's skills surface (.claude/commands/).
func (s *skillsSurface) Kind() agent.SurfaceKind { return agent.SurfaceSkills }

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
	// SelfContainedSkills mirrors agent.SurfaceInputs.SelfContainedSkills: when
	// true, DeliverSkills skips deduping against the delivering machine's
	// ~/.claude/commands, so a portable `profile materialize --target` tree keeps
	// every skill. Every caller but materialize leaves this false.
	SelfContainedSkills bool
}

// Surfaces is claude's set of delivery surfaces for one run, exposed so the
// SurfaceSelection builder can resolve each selected (kind, approach) to a
// surface and hand it to a cell. claude has four surface objects — context, MCP,
// settings (which carries hooks), and skills.
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
		Skills:   &skillsSurface{skills: in.Skills, fs: fs, selfContainedSkills: in.SelfContainedSkills},
	}
}

// Deliveries returns every surface as a plain agent.Delivery, in a stable order,
// for iteration by an isolated cell (worktree / container / materialize target),
// where a well-known write into a private dir is safe.
func (s Surfaces) Deliveries() []agent.Delivery {
	return []agent.Delivery{s.Context, s.MCP, s.Settings, s.Skills}
}

// noopContextDelivery is claude's Hook-approach context delivery (vital-tiger
// v2): a documented no-op. claude's apply-context rides the settings-carried
// SessionStart inject hook + the regenerated cache file, so there is nothing
// extra to write — writing CLAUDE.md too would DOUBLE the context.
type noopContextDelivery struct{}

// Deliver writes nothing and returns a nil handle: the caller's nil-handle-skip
// convention (see contextSurface.Kind's siblings) treats this exactly like any
// other no-op delivery.
func (noopContextDelivery) Deliver(string) (agent.Delivered, error) { return nil, nil }

// Kind reports the context surface (Hook is one of context's approaches).
func (noopContextDelivery) Kind() agent.SurfaceKind { return agent.SurfaceContext }

// claudeApproaches is claude's DECLARED per-surface approach table (vital-tiger
// v2 per-provider dispatch): context supports all three — the native file
// (FIRST = the WithEverything default; SystemPrompt and Hook are explicit caller
// choices — SystemPrompt names the SHARED-cwd scratch conversion, Hook is apply's
// hook-carried context), the out-of-cwd system-prompt scratch, and the
// settings-carried hook (a no-op write). mcp/settings/skills support only the
// native file. The mechanical lookups ride agent.ApproachTable; only this table
// is claude's.
var claudeApproaches = agent.ApproachTable{
	agent.SurfaceContext:  {agent.ApproachUnsafeFile, agent.ApproachSystemPrompt, agent.ApproachHook},
	agent.SurfaceMCP:      {agent.ApproachUnsafeFile},
	agent.SurfaceSettings: {agent.ApproachUnsafeFile},
	agent.SurfaceSkills:   {agent.ApproachUnsafeFile},
}

// SupportedApproaches reports claude's declared approach table for kind.
func (Surfaces) SupportedApproaches(kind agent.SurfaceKind) []agent.Approach {
	return claudeApproaches.Supported(kind)
}

// DefaultApproach reports claude's default (first-declared: the native file)
// approach for kind.
func (Surfaces) DefaultApproach(kind agent.SurfaceKind) (agent.Approach, bool) {
	return claudeApproaches.Default(kind)
}

// SurfaceFor resolves one (kind, approach) to claude's concrete surface. context
// is multi-approach, so its Hook arm is handled here: it resolves to the
// documented no-op (apply's hook-carried context, never a native file), while
// UnsafeFile and SystemPrompt both resolve to the SAME dual-capable
// contextSurface (its Deliver writes CLAUDE.md; its DeliverIsolated — read via
// SharedRealization — writes the out-of-cwd scratch). Everything else rides the
// shared table lookup.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	if kind == agent.SurfaceContext && a == agent.ApproachHook {
		return noopContextDelivery{}, nil
	}
	return claudeApproaches.SurfaceFor("claude", map[agent.SurfaceKind]agent.Delivery{
		agent.SurfaceContext:  s.Context,
		agent.SurfaceMCP:      s.MCP,
		agent.SurfaceSettings: s.Settings,
		agent.SurfaceSkills:   s.Skills,
	}, kind, a)
}

// SharedRealization reports claude's out-of-cwd scratch conversion for context,
// MCP, and settings — the ONLY backend with one (skills has no out-of-cwd flag).
// Each closure runs the SAME DeliverIsolated method already bound to the concrete
// surface instances NewSurfaces built (the ones stashed at ClaudeCode.surfaces),
// never a second Surfaces set — so buildArgs' later Path() read sees the write.
func (s Surfaces) SharedRealization(kind agent.SurfaceKind) (func() (agent.Delivered, error), bool) {
	switch kind {
	case agent.SurfaceContext:
		return s.Context.DeliverIsolated, true
	case agent.SurfaceMCP:
		return s.MCP.DeliverIsolated, true
	case agent.SurfaceSettings:
		return s.Settings.DeliverIsolated, true
	default:
		return nil, false
	}
}

// Compile-time capability contracts. Every surface is a Delivery + KindedDelivery;
// context/MCP/settings ADDITIONALLY offer DeliverIsolated (read as a method value
// by SharedRealization above) — skills has none, so a SHARED-cwd delivery of it
// always falls back to the loud well-known write (proved in surfaces_test.go).
var (
	_ agent.Delivery       = (*contextSurface)(nil)
	_ agent.Delivery       = (*mcpSurface)(nil)
	_ agent.Delivery       = (*settingsSurface)(nil)
	_ agent.Delivery       = (*skillsSurface)(nil)
	_ agent.KindedDelivery = (*contextSurface)(nil)
	_ agent.KindedDelivery = (*mcpSurface)(nil)
	_ agent.KindedDelivery = (*settingsSurface)(nil)
	_ agent.KindedDelivery = (*skillsSurface)(nil)
	_ agent.Delivery       = noopContextDelivery{}
	_ agent.KindedDelivery = noopContextDelivery{}
	_ agent.Placement      = dirPlacement{}
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)
