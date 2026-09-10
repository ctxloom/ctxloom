package claude

import (
	"fmt"
	"slices"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
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
// SurfaceFor / SharedRealization) are the per-provider table the
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
//	commands  | .claude/commands/         | ❌ no out-of-cwd flag → loud native write when shared
//	skills    | .claude/skills/<name>/    | ❌ no out-of-cwd flag → loud native write when shared
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

// Deliver merges context into CLAUDE.md via the ContextWriter core and returns
// a handle whose Cleanup strips the managed section (removing the file when
// nothing user-authored remains) by writing empty context — the honest
// reversal of a MARKER-MERGED write. (Before markers landed, this used
// fs.Remove to reverse a whole-file write; a plain removal would now delete
// hand-authored content that lived outside the markers.) This is the shared
// agent.DeliverManagedContext shape, the same one
// codex's AGENTS.md context surface uses.
func (s *contextSurface) Deliver(dir string) (agent.Delivered, error) {
	return agent.DeliverManagedContext(&ClaudeCodeHookWriter{FS: s.fs}, dir, s.context)
}

// DeliverIsolated writes the framed context file through the reused
// appendFlagDelivery into the out-of-cwd placement; Path then exposes it.
func (s *contextSurface) DeliverIsolated() (agent.Delivered, error) {
	return s.appendD.DeliverContext(s.context)
}

// State implements agent.StateReader: it reports what CLAUDE.md currently
// carries in its managed section, via the shared read-side helper
// (agent.ReadManagedContext) — the same core WriteContext's write side
// merges through (ClaudeCodeHookWriter.WriteContext), so the read and write
// paths cannot disagree about where the managed section lives or how it is
// framed. An absent file or an absent managed section reports
// FileDeliveryState with Found/HasSection false; Currency then turns that
// into the missing verdict. This is claude's half of the engine-delivery
// seam's read side (docs/design/engine-delivery-seam.design.md step 3),
// mirroring the mock backend's mockContextSurface.State.
func (s *contextSurface) State(dir string) (agent.DeliveryState, error) {
	fs := agent.GetFS(s.fs)
	w := &ClaudeCodeHookWriter{FS: fs}
	state, err := agent.ReadManagedContext(fs, w.ContextPath(dir), ContextFileName)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// Path returns the framed <hash>.sysprompt.md written by DeliverIsolated (for
// --append-system-prompt-file), or "" whenever no file stands behind it: before
// delivery, for empty context, and after a FAILED delivery.
func (s *contextSurface) Path() string { return s.appendD.Path() }

// mcpSurface is claude's MCP surface.
//
// Delivery (well-known) writes .mcp.json into the target dir via the reused
// fileTemplateDelivery.DeliverMCP. DeliverIsolated writes the same merged
// .mcp.json into the out-of-cwd placement and exposes its path (Path) for
// --mcp-config <file> (paired with --strict-mcp-config, which replaces the
// project .mcp.json rather than merging — a buildArgs concern).
type mcpSurface struct {
	bundle          map[string]wire.MCPServer
	fs              afero.Fs
	isolated        agent.Placement // out-of-cwd location for the --mcp-config file
	path            string          // set by DeliverIsolated: the out-of-cwd .mcp.json
	commandOverride string          // see SurfaceInputs.MCPCommandOverride
}

// deliver is the ONE .mcp.json recipe both entry points run: build the reused
// file-template writer against dir, thread the ctxloom-MCP command override
// onto it (settingsSurface has no analogous knob, since hooks + statusline
// carry no stdio command), and write the merged config. Deliver and
// DeliverIsolated differ only in where dir comes from and whether the resulting
// path is recorded; that is the whole of what either entry point adds.
// reprise:accept-drift — shares a three-line shape with settingsSurface.deliver and commandsSurface.Deliver, and that shape IS the whole body: construct the writer, set the one knob this surface owns, call the one delivery it owns. A helper taking both as parameters is longer than what it replaces and hides which knob belongs to which surface; each of the three changes only when its own surface's knob or delivery changes.
func (s *mcpSurface) deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs)
	d.mcpCommandOverride = s.commandOverride
	return d.DeliverMCP(s.bundle)
}

// Deliver writes .mcp.json into dir via the reused file-template MCP writer.
func (s *mcpSurface) Deliver(dir string) (agent.Delivered, error) { return s.deliver(dir) }

// DeliverIsolated writes the merged .mcp.json into the out-of-cwd placement and
// records its path for --mcp-config. A FAILED write clears that path: Path()
// promises "" for a file that does not exist, and flagArgs must never hand
// claude --mcp-config naming one.
func (s *mcpSurface) DeliverIsolated() (agent.Delivered, error) {
	dir := s.isolated.Dir()
	handle, err := s.deliver(dir)
	if err != nil {
		s.path = ""
		return nil, err
	}
	// The recorded path comes from the DECLARED presentation, not from a second
	// hand-written join: mcpSurface's declaration is what --mcp-config is
	// pointed at, so a wrong rel path in it cannot pass unnoticed.
	s.path = claudePresentation(agent.SurfaceMCP, agent.ApproachUnsafeFile, dir).HostPath
	return handle, nil
}

// Path returns the out-of-cwd .mcp.json written by DeliverIsolated (for
// --mcp-config <file>), or "" before delivery and after a FAILED one.
func (s *mcpSurface) Path() string { return s.path }

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
	// denyTools is the resolved deny_tools union (SurfaceInputs.DenyTools) —
	// per-tool identifiers (e.g. "Task") this run's settings.json denies via
	// permissions.deny. Threaded to fileTemplateDelivery as a RECEIVER field
	// (below), mirroring mcpSurface.commandOverride, so DeliverSettings's
	// signature stays exactly agent.SettingsDelivery — no cross-module
	// interface change for an engine-specific extra.
	denyTools []string
	fs        afero.Fs
	isolated  agent.Placement // out-of-cwd location for the --settings file
	path      string          // set by DeliverIsolated: the out-of-cwd settings.json
}

// deliver is the ONE .claude/settings.json recipe both entry points run (see
// mcpSurface.deliver for the shape): build the reused file-template writer
// against dir, thread the resolved deny_tools union onto it, and write the
// settings JSON including hooks and the statusline policy.
func (s *settingsSurface) deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs)
	d.denyTools = s.denyTools
	return d.DeliverSettings(s.hooks, s.manageStatusline)
}

// Deliver writes .claude/settings.json into dir via the reused file-template
// settings writer.
func (s *settingsSurface) Deliver(dir string) (agent.Delivered, error) { return s.deliver(dir) }

// DeliverIsolated writes the settings JSON (incl. hooks) into the out-of-cwd
// placement and records its path for --settings. A FAILED write clears that
// path, for the same reason mcpSurface's does: no --settings flag may name a
// file that was not written.
func (s *settingsSurface) DeliverIsolated() (agent.Delivered, error) {
	dir := s.isolated.Dir()
	handle, err := s.deliver(dir)
	if err != nil {
		s.path = ""
		return nil, err
	}
	// Declared, not re-joined — see mcpSurface.DeliverIsolated.
	s.path = claudePresentation(agent.SurfaceSettings, agent.ApproachUnsafeFile, dir).HostPath
	return handle, nil
}

// Path returns the out-of-cwd settings.json written by DeliverIsolated (for
// --settings <file>), or "" before delivery and after a FAILED one.
func (s *settingsSurface) Path() string { return s.path }

// commandsSurface is claude's commands surface: the slash-command exports under
// .claude/commands/. claude has no out-of-cwd flag for slash-commands and no
// SharedRealization (see Surfaces.SharedRealization below), so a SHARED-cwd
// delivery of it falls back to the loud well-known write; first preference is
// always an isolated cell. It self-describes via UnsafeInfo for that fallback's
// warning. (Unlike the other engines, claude's commands ride
// fileTemplateDelivery.DeliverCommands, which owns its own cleanup, so they are
// NOT the shared agent.ManagedCommandsDelivery.)
type commandsSurface struct {
	commands              []agent.CommandExport
	fs                    afero.Fs
	selfContainedCommands bool // mirrors SurfaceInputs.SelfContainedCommands; see DeliverCommands
}

// Deliver writes .claude/commands/ into dir via the reused file-template
// commands writer. selfContainedCommands rides along so a materialize target
// (a portable, self-contained tree) skips deduping against the delivering
// machine's ~/.claude/commands — see fileTemplateDelivery.DeliverCommands.
func (s *commandsSurface) Deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs)
	d.selfContainedCommands = s.selfContainedCommands
	return d.DeliverCommands(s.commands)
}

// UnsafeInfo returns claude's commands identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *commandsSurface) UnsafeInfo() string { return "claude/commands" }

// newSkillsSurface builds claude's skills surface: an
// agent.ManagedSkillPackagesDelivery bound to WriteSkillFiles (skillfiles.go),
// exactly as commandsSurface wraps WriteCommandFiles — the shared delivery
// type is reusable here (unlike commands) because claude's skill writer needs
// no home-dir dedup or DeliverIsolated variant: no engine has an out-of-cwd
// flag for a skill package, matching commands' own "no SharedRealization"
// (see Surfaces.SharedRealization below). SurfaceInputs no longer carries a
// SelfContainedSkills knob (it was deleted): it was threaded in from
// three call sites and read by nothing here — claude's WriteSkillFiles has no
// dedup to opt out of, so there was nothing for it to control.
func newSkillsSurface(skills []agent.SkillExport, fs afero.Fs) *agent.ManagedSkillPackagesDelivery {
	return agent.NewManagedSkillPackagesDelivery("claude/skills", skills, func(dir string, skills []agent.SkillExport) error {
		return WriteSkillFiles(dir, skills, agent.WithCommandFS(fs))
	})
}

// Surfaces is claude's set of delivery surfaces for one run, exposed so the
// SurfaceSelection builder can resolve each selected (kind, approach) to a
// surface and hand it to a cell. claude has five surface objects — context,
// MCP, settings (which carries hooks), commands, and skills.
type Surfaces struct {
	Context  *contextSurface
	MCP      *mcpSurface
	Settings *settingsSurface
	Commands *commandsSurface
	Skills   *agent.ManagedSkillPackagesDelivery

	// dispatch is the per-kind lookup SurfaceFor resolves against, built once
	// here (not reallocated per SurfaceFor call) since it never changes after
	// construction.
	dispatch map[agent.SurfaceKind]agent.Delivery
}

// NewSurfaces builds claude's surfaces from a run's inputs. isolated is the
// per-run, OUT-OF-CWD placement the race-safe variants write into (the
// append-flag file, the --mcp-config file, the --settings file); a nil fs
// defaults to the OS filesystem. Every surface's well-known Delivery takes its
// target dir at call time, so only the race-safe variants bind isolated here.
//
// It takes the SHARED agent.SurfaceInputs, exactly as kiro/opencode
// do. A local copy of it (agent.SurfaceInputs minus Fragments) would force two
// hand-maintained field-by-field mappers — claudecode.go's buildSurfaces and
// lm/backends/registry.go's newSurfaces closure — and those drift: one copied
// ten of the eleven fields and silently dropped MCPCommandOverride. Reading the
// shared struct directly makes that class of drop impossible; claude simply
// ignores the fields it has no use for (Fragments, AgentName).
func NewSurfaces(in agent.SurfaceInputs, isolated agent.Placement, fs afero.Fs) Surfaces {
	fs = agent.GetFS(fs)
	context := newContextSurface(in.Context, isolated, fs)
	mcp := &mcpSurface{bundle: in.BundleMCP, fs: fs, isolated: isolated, commandOverride: in.MCPCommandOverride}
	settings := &settingsSurface{hooks: in.Hooks, manageStatusline: in.ManageStatusline, denyTools: in.DenyTools, fs: fs, isolated: isolated}
	commands := &commandsSurface{commands: in.Commands, fs: fs, selfContainedCommands: in.SelfContainedCommands}
	skills := newSkillsSurface(in.Skills, fs)
	return Surfaces{
		Context:  context,
		MCP:      mcp,
		Settings: settings,
		Commands: commands,
		Skills:   skills,
		dispatch: map[agent.SurfaceKind]agent.Delivery{
			agent.SurfaceContext:  context,
			agent.SurfaceMCP:      mcp,
			agent.SurfaceSettings: settings,
			agent.SurfaceCommands: commands,
			agent.SurfaceSkills:   skills,
		},
	}
}

// noopContextDelivery is claude's Hook-approach context delivery: a
// documented no-op. claude's apply-context rides the settings-carried
// SessionStart inject hook + the regenerated cache file, so there is nothing
// extra to write — writing CLAUDE.md too would DOUBLE the context.
type noopContextDelivery struct{}

// Deliver writes nothing and returns a nil handle: the caller's nil-handle-skip
// convention (see contextSurface.Kind's siblings) treats this exactly like any
// other no-op delivery.
func (noopContextDelivery) Deliver(string) (agent.Delivered, error) { return nil, nil }

// claudeStart begins a presentation composition rooted at dir.
//
// Every claude presenter roots UnderProjectRoot, and that is what lets ONE
// declaration serve both deliveries of a surface: the well-known write passes
// the project dir, while the out-of-cwd realization passes the per-run
// placement dir. The isolated case is therefore a different ROOT, not a second
// set of presenters — which is why SharedRealization below needs no
// declarations of its own.
//
// present.OnHost, not present.Containerize: containerization is PRE-advice
// applied once per run, before any Start exists, so a Presenter is
// structurally unable to branch on it (see agent.Presenter's doc). This
// composes the host view; a containerized caller advises the Paths before
// New ever runs.
func claudeStart(dir string) present.Start {
	return present.New(present.OnHost(present.Paths{ProjectRoot: present.Root{Host: dir}}))
}

// claudeContextFilePresenter declares the native CLAUDE.md the engine reads
// directly. No flag: an engine started in this dir finds it by name.
func claudeContextFilePresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(ContextFileName).Build()
}

// claudeContextSystemPromptPresenter declares the out-of-cwd framed context
// consumed via --append-system-prompt-file.
//
// It roots at the placement dir ITSELF rather than at a filename, and that is
// a statement about what is knowable: appendFlagDelivery names the file
// <hash>.sysprompt.md where <hash> is a sha256 prefix over the FRAMED BYTES,
// so the leaf cannot be declared ahead of the content. What this presentation
// contributes is therefore the FLAG, which flagArgs reads; the path it is
// paired with comes from contextSurface.Path() after the write. Declaring a
// fixed leaf here would be a lie that nothing would catch.
func claudeContextSystemPromptPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot("").AnnounceFlag(flagAppendSystemFile).Build()
}

// claudeContextHookPresenter declares the hook-carried context arm: the bytes
// ride the SessionStart inject hook inside .claude/settings.json, so this
// presentation names that settings file and announces no flag of its own.
// Its Delivery is a documented no-op (see SurfaceFor).
func claudeContextHookPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(relSettings).Build()
}

// claudeMCPPresenter declares .mcp.json plus the --mcp-config flag its
// out-of-cwd realization is announced with.
func claudeMCPPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(MCPFileName).AnnounceFlag(flagMCPConfig).Build()
}

// claudeSettingsPresenter declares .claude/settings.json (hooks + statusline)
// plus the --settings flag its out-of-cwd realization is announced with.
func claudeSettingsPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(relSettings).AnnounceFlag(flagSettings).Build()
}

// claudeCommandsPresenter declares .claude/commands/. No flag: claude has no
// out-of-cwd redirect for slash commands.
func claudeCommandsPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(relCommands).Build()
}

// claudeSkillsPresenter declares .claude/skills/. No flag, for the same reason
// commands has none.
func claudeSkillsPresenter(s present.Start) present.Presentation {
	return s.UnderProjectRoot(relSkills).Build()
}

// claudePresentations is claude's DECLARED per-surface presentation table, and
// the ONE place claude's surface membership is stated: SupportedApproaches,
// DefaultApproach and SurfaceFor all read it rather than repeating the list.
//
// context is the one multi-delivery surface — the native file (the DEFAULT,
// named explicitly rather than inferred from declaration order), the
// out-of-cwd system-prompt scratch, and the settings-carried hook. Every other
// surface has exactly one delivery.
//
// A name here is known IF AND ONLY IF a Presenter is registered under it, so
// "supported" and "constructible" cannot disagree — which is the whole reason
// this replaced a capability list beside a construction map.
var claudePresentations = map[agent.SurfaceKind]agent.Presentations{
	agent.SurfaceContext: agent.Presents("claude", agent.SurfaceContext, agent.ApproachUnsafeFile.String(), claudeContextFilePresenter).
		Or(agent.ApproachSystemPrompt.String(), claudeContextSystemPromptPresenter).
		Or(agent.ApproachHook.String(), claudeContextHookPresenter),
	agent.SurfaceMCP:      agent.Presents("claude", agent.SurfaceMCP, agent.ApproachUnsafeFile.String(), claudeMCPPresenter),
	agent.SurfaceSettings: agent.Presents("claude", agent.SurfaceSettings, agent.ApproachUnsafeFile.String(), claudeSettingsPresenter),
	agent.SurfaceCommands: agent.Presents("claude", agent.SurfaceCommands, agent.ApproachUnsafeFile.String(), claudeCommandsPresenter),
	agent.SurfaceSkills:   agent.Presents("claude", agent.SurfaceSkills, agent.ApproachUnsafeFile.String(), claudeSkillsPresenter),
}

// claudePresentation resolves ONE surface's declared presentation for
// (kind, a) against dir. Callers must hold a kind claudePresentations
// declares; an undeclared kind has no Presenter to run.
func claudePresentation(kind agent.SurfaceKind, a agent.Approach, dir string) present.Presentation {
	return claudePresentations[kind].Resolve(a.String(), claudeStart(dir))
}

// SupportedApproaches implements SurfaceSet.SupportedApproaches, DERIVED from
// claudePresentations rather than a second declared list — a kind absent from
// the table reports nil (absent/folded), never an error.
func (s Surfaces) SupportedApproaches(kind agent.SurfaceKind) []agent.Approach {
	d, ok := claudePresentations[kind]
	if !ok {
		return nil
	}
	return agent.ApproachesFor(d)
}

// DefaultApproach implements SurfaceSet.DefaultApproach: the Presentations'
// own NAMED default, translated back to the shared Approach vocabulary. It is
// a named key rather than "the first entry declared", so no literal's order is
// load-bearing here.
func (s Surfaces) DefaultApproach(kind agent.SurfaceKind) (agent.Approach, bool) {
	d, ok := claudePresentations[kind]
	if !ok {
		return 0, false
	}
	a, err := agent.ParseApproach(d.Default())
	if err != nil {
		return 0, false
	}
	return a, true
}

// SurfaceFor resolves one (kind, approach) to claude's concrete surface.
// context is multi-delivery, so its Hook arm is handled here: it resolves to
// the documented no-op (apply's hook-carried context, never a native file),
// while UnsafeFile and SystemPrompt both resolve to the SAME dual-capable
// contextSurface (its Deliver writes CLAUDE.md; its DeliverIsolated — read via
// SharedRealization — writes the out-of-cwd scratch). Everything else is
// validated against the DECLARED presentations and then looked up.
func (s Surfaces) SurfaceFor(kind agent.SurfaceKind, a agent.Approach) (agent.Delivery, error) {
	if kind == agent.SurfaceContext && a == agent.ApproachHook {
		return noopContextDelivery{}, nil
	}
	d, ok := claudePresentations[kind]
	if !ok {
		return nil, fmt.Errorf("claude: no %s surface", kind)
	}
	if !slices.Contains(agent.ApproachesFor(d), a) {
		return nil, fmt.Errorf("claude: no %s surface via %s", kind, a)
	}
	del, ok := s.dispatch[kind]
	if !ok {
		return nil, fmt.Errorf("claude: no %s surface", kind)
	}
	return del, nil
}

// SharedRealization reports claude's out-of-cwd scratch conversion for the
// (kind, approach) pair — the ONLY backend with one (commands/skills have no
// out-of-cwd flag). It is PAIR-keyed, not kind-alone: context is the
// one multi-approach kind claude realizes, and only ApproachSystemPrompt does —
// ApproachUnsafeFile is the caller's explicit request for the native CLAUDE.md
// write, so it reports no realization and deliverOneShared falls to the
// well-known write (loudly warned; this is the honor-with-warning fork of
// that decision). ApproachHook resolves to the documented no-op, which never carries
// DeliverIsolated, so deliverOneShared's isolatedDelivery guard never even
// reaches this switch for it. mcp/settings have exactly one approach each
// (ApproachUnsafeFile), and it is the one that realizes — the pair-key changes
// nothing for them, so their --mcp-config/--settings launch flags keep firing.
// Each closure runs the SAME DeliverIsolated method already bound to the
// concrete surface instances NewSurfaces built (the ones stashed at
// ClaudeCode.surfaces), never a second Surfaces set — so buildArgs' later
// Path() read sees the write.
func (s Surfaces) SharedRealization(kind agent.SurfaceKind, a agent.Approach) (func() (agent.Delivered, error), bool) {
	switch {
	case kind == agent.SurfaceContext && a == agent.ApproachSystemPrompt:
		return s.Context.DeliverIsolated, true
	case kind == agent.SurfaceMCP && a == agent.ApproachUnsafeFile:
		return s.MCP.DeliverIsolated, true
	case kind == agent.SurfaceSettings && a == agent.ApproachUnsafeFile:
		return s.Settings.DeliverIsolated, true
	default:
		return nil, false
	}
}

// Compile-time capability contracts. Every surface is a Delivery + KindedDelivery;
// context/MCP/settings ADDITIONALLY offer DeliverIsolated (read as a method value
// by SharedRealization above) — commands has none, so a SHARED-cwd delivery of it
// always falls back to the loud well-known write (proved in surfaces_test.go).
var (
	_ agent.Delivery    = (*contextSurface)(nil)
	_ agent.StateReader = (*contextSurface)(nil)
	_ agent.Delivery    = (*mcpSurface)(nil)
	_ agent.Delivery    = (*settingsSurface)(nil)
	_ agent.Delivery    = (*commandsSurface)(nil)
	_ agent.Delivery    = (*agent.ManagedSkillPackagesDelivery)(nil)
	_ agent.Delivery    = noopContextDelivery{}
	_ agent.Placement   = dirPlacement{}
	// Surfaces exposes Deliveries (for an isolated cell) + the approach-aware
	// dispatch (SupportedApproaches / DefaultApproach / SurfaceFor /
	// SharedRealization), so it satisfies agent.SurfaceSet.
	_ agent.SurfaceSet = Surfaces{}
)

// flagArgs returns the out-of-cwd launch flags for the surfaces this run
// actually delivered: --append-system-prompt-file, --mcp-config and --settings,
// each paired with the path its surface recorded. A surface reports "" (or is
// absent from a zero-value set, before Setup) when it delivered nothing, and
// contributes no flag at all — claude must not be handed a flag naming a file
// that was never written.
//
// Order is fixed (context, MCP, settings) because argv order is observable: a
// VARIADIC claude flag landing last before a positional swallows it (see
// buildArgs' prompt terminator).
func (s Surfaces) flagArgs() []string {
	var args []string
	// announced pairs the flag the surface's presentation DECLARES with the
	// path that surface actually wrote. The flag name is read from the
	// declaration rather than from the constant directly, which is what makes
	// the presenters load-bearing: change a declared flag and this argv
	// changes with it. The path stays the runtime one because context's leaf
	// is content-derived (see claudeContextSystemPromptPresenter).
	announced := func(kind agent.SurfaceKind, a agent.Approach, path string) {
		if path == "" {
			return
		}
		p := claudePresentation(kind, a, "")
		if len(p.Args) == 0 {
			return
		}
		args = append(args, p.Args[0], path)
	}
	if s.Context != nil {
		announced(agent.SurfaceContext, agent.ApproachSystemPrompt, s.Context.Path())
	}
	if s.MCP != nil {
		announced(agent.SurfaceMCP, agent.ApproachUnsafeFile, s.MCP.Path())
	}
	if s.Settings != nil {
		announced(agent.SurfaceSettings, agent.ApproachUnsafeFile, s.Settings.Path())
	}
	return args
}
