package claude

import (
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file is claude's DECLARATION on the unified surface-delivery seam
// (internal/shared/agent/cells.go, declaration.go): each approach claude
// supports as a value implementing agent.Approach, and Surfaces — the one
// place claude's surface membership is stated. Every approach here WRAPS an
// existing claude writer verbatim — appendFlagDelivery (contextdelivery.go),
// fileTemplateDelivery (surfacedelivery.go), and the ContextWriter core
// WriteContext (claude.go); the record-backed settings approach, which
// writes through confpatch instead, lives in surfaces_hewrecord.go.
// buildArgs (claudecode.go) reads each flag-announced approach's Path() after
// delivery, on every cell.
//
// Capability recap. A surface with TWO approaches names both: which one runs
// is the caller's selection (or, on a shared launch with no preference, the
// derivation in preferOutOfCwd), never a conversion applied underneath it.
//
//	surface   | approaches                                  | where its bytes land
//	----------|---------------------------------------------|----------------------------------------
//	context   | unsafe-file (default) / system-prompt / hook | CLAUDE.md / <hash>.sysprompt.md announced on --append-system-prompt-file / rides settings
//	MCP       | .mcp.json                                   | project file, announced on --mcp-config when out of cwd
//	settings  | unsafe-file (default) / hew-record          | .claude/settings.json (--settings when out of cwd) / <EngineHome>/settings.json
//	commands  | .claude/commands/                           | ❌ no flag form → loud native write when shared
//	skills    | .claude/skills/<name>/                      | ❌ no flag form → loud native write when shared
//
// claude folds "settings + hooks" into ONE surface because claude's hooks live
// inside .claude/settings.json — there is no separate hooks file to deliver.

// ApproachSystemPrompt names claude's out-of-cwd framed context consumed via
// --append-system-prompt-file — semantically distinct from the native file
// (the content enters the system prompt, not project memory). It is claude's
// own name, declared here and nowhere shared: no other engine has it.
const ApproachSystemPrompt = "system-prompt"

// placement is where a file-writing strategy writes: the reused writers
// (fileTemplateDelivery, appendFlagDelivery) hold one, injected at
// construction, never passed as a method parameter.
type placement interface {
	// Dir returns the directory the strategy writes into.
	Dir() string
}

// dirPlacement is a trivial placement whose Dir() returns a fixed directory.
// It adapts a root read from the advised Start into the placement the reused
// writers construct against — the project root for the well-known Delivery,
// the Scratch root for the out-of-cwd forms; both arrive at call time, never
// at construction.
type dirPlacement struct{ dir string }

// Dir returns the fixed directory this placement wraps.
func (p dirPlacement) Dir() string { return p.dir }

// privateRoot is the ONE place claude decides which advised root a run's
// CONFIGURATION lands under — the framed system prompt and the default
// .mcp.json, neither of which may be written into the shared live cwd.
// Every such approach reads it rather than naming a root itself, so the
// choice is made once and cannot drift between two surfaces that are supposed
// to obey one rule.
//
// It is Scratch TODAY, and that is a placement decision with a known cost, not
// the final one. The ruled destination is the run-specific RELOCATED ENGINE
// HOME — configuration is a property of the run, mounted into the container
// and inited against — but only a run whose binding advises that home resolves
// EngineHome at all: a container cell and a default shared `ctxloom run`
// currently advise none, so rooting here at EngineHome would turn those into
// refusals rather than deliveries. Scratch is advised on all three cell kinds,
// so it is the root that keeps this decision honest until every cell has a
// relocated home; when one does, this function is the only thing that changes.
//
// KNOWN COST, recorded so it is not rediscovered: on an ISOLATED cell Scratch
// IS the working directory, so the framed file lands in the checkout root for
// the life of the run (the Delivered handle removes it). It is private there —
// the checkout is per-agent — so it races nothing; it is merely visible.
func privateRoot(start present.Start) present.Root { return start.Paths().Scratch }

// underPrivateRoot roots a PRESENTATION at rel beneath the same root
// privateRoot names. It sits here, adjacent to privateRoot and nowhere else,
// because the two must move together: the presenter states where the bytes go
// and Deliver puts them there, so a flip that changed one and not the other
// would announce a path nothing was written to. They are checked against each
// other by TestSurfaces_PresentedPathIsWhereTheApproachWrites.
func underPrivateRoot(start present.Start, rel string) present.Rooted {
	return start.UnderScratch(rel)
}

// claudeContextWriter is the ContextWriter the native-file context approach
// merges through — the same core WriteContext (claude.go) every CLAUDE.md
// write uses.
func claudeContextWriter(fs afero.Fs) agent.ContextWriter { return &ClaudeCodeHookWriter{FS: fs} }

// newMCPWriter builds the .mcp.json recipe both MCP approaches embed, from the
// one set of run inputs. It exists so the two constructors cannot come apart
// on a field: MCPCommandOverride was already once lost that way.
func newMCPWriter(in agent.SurfaceInputs, fs afero.Fs) mcpWriter {
	return mcpWriter{bundle: in.BundleMCP, fs: agent.GetFS(fs), commandOverride: in.MCPCommandOverride}
}

// systemPromptContext is claude's system-prompt context approach.
//
// It writes the framed <hash>.sysprompt.md beneath the run's private root via
// the existing appendFlagDelivery and exposes its path (Path) for
// --append-system-prompt-file. It is LaunchOnly: at rest there is no argv sink
// for the flag, so DeliverUnder refuses it.
//
// It has exactly ONE form, on every cell. It previously had two, and they were
// named backwards from the cell that ran them: the plain Deliver was the
// CLAUDE.md write — the same write the native-file approach performs — so an
// ISOLATED launch (worktree or container) that had selected system-prompt was
// silently handed project memory instead, while only a SHARED launch got the
// framed file. The content reaches the engine by a different mechanism under a
// different name, which is a different product behaviour, not a placement
// detail.
//
// The substitution is gone rather than redirected, because a surface is the
// wrong place to decide one: the system prompt does not know what it would be
// degrading to, or whether the caller would have accepted CLAUDE.md instead.
// That is the engine declaration's job. A run that cannot serve this approach
// gets ErrUnrootedScratch and writes nothing.
type systemPromptContext struct {
	content string
	fs      afero.Fs
	path    string // set by Deliver: the framed context file under the private root
}

// LaunchOnly marks the approach as refused at rest.
func (*systemPromptContext) LaunchOnly() {}

// Present declares the out-of-cwd form's flag. It roots at the Scratch dir
// ITSELF rather than at a filename, and that is a statement about what is
// knowable: appendFlagDelivery names the file <hash>.sysprompt.md where <hash>
// is a sha256 prefix over the FRAMED BYTES, so the leaf is paired from Path()
// after the write. What this presentation contributes is the FLAG, which
// flagArgs reads.
func (s *systemPromptContext) Present(start present.Start) present.Presentation {
	return underPrivateRoot(start, "").AnnounceFlag(flagAppendSystemFile).Build()
}

// Deliver writes the framed context file through the reused appendFlagDelivery
// beneath the advised private root; Path then exposes it for
// --append-system-prompt-file. This is the approach's ONLY form, so every cell
// reaches it — an isolated launch that selected system-prompt now gets the
// system prompt.
//
// An unresolved private root REFUSES (ErrUnrootedScratch) rather than writing
// the well-known file instead; see the type doc. A FAILED write leaves Path ""
// (the writer's own contract): no flag may name a file that was not written.
func (s *systemPromptContext) Deliver(start present.Start) (agent.Delivered, error) {
	if err := agent.ScratchRooted(start); err != nil {
		return nil, err
	}
	d := newAppendFlagDelivery(dirPlacement{dir: privateRoot(start).Host}, s.fs)
	handle, err := d.DeliverContext(s.content)
	s.path = d.Path()
	return handle, err
}

// Path returns the framed <hash>.sysprompt.md written by Deliver (for
// --append-system-prompt-file), or "" whenever no file stands behind it: before
// delivery, for empty context, and after a FAILED delivery.
func (s *systemPromptContext) Path() string { return s.path }

// ApproachMCPConfig names claude's PRIVATE MCP config file, announced on
// --mcp-config. It is claude's own name, declared here and nowhere shared, and
// it is the DEFAULT: a run's MCP set is ctxloom's to deliver, and delivering it
// by writing the user's project .mcp.json is a shared/dangerous avenue nothing
// should take by default.
const ApproachMCPConfig = "mcp-config"

// mcpWriter is the ONE .mcp.json recipe both MCP approaches run: build the
// reused file-template writer against dir, thread the ctxloom-MCP command
// override onto it (settingsSurface has no analogous knob, since hooks +
// statusline carry no stdio command), and write the merged config.
//
// The two approaches EMBED it and differ only in where the bytes go and what
// the engine is told — which is the whole of what distinguishes them. Sharing
// the writer rather than the type is what keeps "one .mcp.json recipe" true
// while still letting the two say different things about placement: a single
// type with two methods is what made the silent conversion expressible in the
// first place.
type mcpWriter struct {
	bundle          map[string]wire.MCPServer
	fs              afero.Fs
	commandOverride string // see SurfaceInputs.MCPCommandOverride
}

// deliver writes the merged .mcp.json into dir.
// reprise:accept-drift — shares a three-line shape with settingsSurface.deliver and commandsSurface.Deliver, and that shape IS the whole body: construct the writer, set the one knob this surface owns, call the one delivery it owns. A helper taking both as parameters is longer than what it replaces and hides which knob belongs to which surface; each of the three changes only when its own surface's knob or delivery changes.
func (w mcpWriter) deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, w.fs)
	d.mcpCommandOverride = w.commandOverride
	return d.DeliverMCP(w.bundle)
}

// servers exposes the bundle for mcpServerNames, which needs the server set
// regardless of WHICH approach delivered it.
func (w mcpWriter) servers() map[string]wire.MCPServer { return w.bundle }

// mcpConfig is claude's DEFAULT MCP approach: the merged .mcp.json beneath the
// run's private root, announced on --mcp-config <file>. Used WITHOUT
// --strict-mcp-config, so claude LAYERS ctxloom's servers over the user's own
// project .mcp.json rather than replacing it (a buildArgs concern).
//
// It is LaunchOnly: at rest there is no argv sink for the flag, so DeliverUnder
// refuses it and the at-rest callers name the project file explicitly.
//
// This approach and mcpUnsafeFile used to be two FORMS of one type, and a
// shared-cwd delivery ran the private form whichever the caller had named —
// so a caller that explicitly asked for the project .mcp.json got this instead
// and was told it succeeded. They are separate approaches now: which one runs
// is the selection, never a conversion applied underneath it.
type mcpConfig struct {
	mcpWriter
	path string // set by Deliver: the private .mcp.json
}

// LaunchOnly marks the approach as refused at rest.
func (*mcpConfig) LaunchOnly() {}

// Present declares the private .mcp.json and the flag it is announced with.
func (s *mcpConfig) Present(start present.Start) present.Presentation {
	return underPrivateRoot(start, MCPFileName).AnnounceFlag(flagMCPConfig).Build()
}

// Deliver writes the merged .mcp.json beneath the advised private root and
// records its path for --mcp-config. An unresolved private root REFUSES
// (ErrUnrootedScratch) rather than falling back to the project file — the
// fallback IS the defect. A FAILED write clears the path: Path() promises ""
// for a file that does not exist, and flagArgs must never hand claude
// --mcp-config naming one.
func (s *mcpConfig) Deliver(start present.Start) (agent.Delivered, error) {
	if err := agent.ScratchRooted(start); err != nil {
		return nil, err
	}
	handle, err := s.deliver(privateRoot(start).Host)
	if err != nil {
		s.path = ""
		return nil, err
	}
	// The recorded path comes from the DECLARED leaf, not from a second
	// hand-written join: it is what --mcp-config is pointed at, so a wrong rel
	// path cannot pass unnoticed.
	s.path = underPrivateRoot(start, MCPFileName).Build().HostPath
	return handle, nil
}

// Path returns the private .mcp.json written by Deliver (for --mcp-config
// <file>), or "" before delivery and after a FAILED one.
func (s *mcpConfig) Path() string { return s.path }

// mcpUnsafeFile is claude's project-file MCP approach: the merged .mcp.json
// written to the well-known path in the project root, which claude reads
// directly — so it announces no flag.
//
// It is reachable ONLY by naming it. On a SHARED cwd it is warned and
// performed, exactly as context:unsafe-file is and through the very same path
// (deliverOneShared's warning), because it is the same decision about the same
// kind of file: one rule for both surfaces. Into an isolated cell it is simply
// the native write, race-free by construction.
type mcpUnsafeFile struct{ mcpWriter }

// Present declares the well-known project .mcp.json. No flag: claude reads
// this path itself, and announcing it as well would load the same servers
// twice.
func (s *mcpUnsafeFile) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(MCPFileName).Build()
}

// Deliver writes .mcp.json beneath the advised project root.
func (s *mcpUnsafeFile) Deliver(start present.Start) (agent.Delivered, error) {
	return s.deliver(start.Paths().ProjectRoot.Host)
}

// UnsafeInfo returns claude's MCP identity for the shared-cwd warning
// (deliverOneShared's unsafeNamed check, cells.go).
func (s *mcpUnsafeFile) UnsafeInfo() string { return "claude/mcp" }

// settingsSurface is claude's settings approach (hooks + statusline; claude
// keeps them in a single .claude/settings.json).
//
// Deliver (well-known) writes .claude/settings.json into the project root via
// the reused fileTemplateDelivery.DeliverSettings. DeliverIsolated writes the
// same settings JSON beneath Scratch and exposes its path (Path) for
// --settings <file>.
type settingsSurface struct {
	hooks            *wire.HooksConfig
	manageStatusline bool
	// denyTools is the resolved deny_tools union (SurfaceInputs.DenyTools) —
	// per-tool identifiers (e.g. "Task") this run's settings.json denies via
	// permissions.deny. Threaded to fileTemplateDelivery as a RECEIVER field
	// (below), mirroring mcpWriter.commandOverride, so DeliverSettings's
	// signature stays unchanged for an engine-specific extra.
	denyTools []string
	fs        afero.Fs
	path      string // set by DeliverIsolated: the out-of-cwd settings.json
}

// Present declares .claude/settings.json (hooks + statusline) plus the
// --settings flag its out-of-cwd form is announced with.
func (s *settingsSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(relSettings).AnnounceFlag(flagSettings).Build()
}

// deliver is the ONE .claude/settings.json recipe both entry points run (see
// mcpWriter.deliver for the shape): build the reused file-template writer
// against dir, thread the resolved deny_tools union onto it, and write the
// settings JSON including hooks and the statusline policy.
func (s *settingsSurface) deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs)
	d.denyTools = s.denyTools
	return d.DeliverSettings(s.hooks, s.manageStatusline)
}

// Deliver writes .claude/settings.json beneath the advised project root via
// the reused file-template settings writer.
func (s *settingsSurface) Deliver(start present.Start) (agent.Delivered, error) {
	return s.deliver(start.Paths().ProjectRoot.Host)
}

// DeliverIsolated writes the settings JSON (incl. hooks) beneath the advised
// Scratch root and records its path for --settings. A FAILED write clears
// that path, for the same reason mcpConfig.Deliver does: no --settings flag may
// name a file that was not written.
func (s *settingsSurface) DeliverIsolated(start present.Start) (agent.Delivered, error) {
	handle, err := s.deliver(start.Paths().Scratch.Host)
	if err != nil {
		s.path = ""
		return nil, err
	}
	// Declared, not re-joined — see mcpConfig.Deliver.
	s.path = start.UnderScratch(relSettings).Build().HostPath
	return handle, nil
}

// Path returns the out-of-cwd settings.json written by DeliverIsolated (for
// --settings <file>), or "" before delivery and after a FAILED one.
func (s *settingsSurface) Path() string { return s.path }

// commandsSurface is claude's commands approach: the slash-command exports
// under .claude/commands/. claude has no out-of-cwd flag for slash-commands
// (no OutOfCwd form), so a SHARED-cwd delivery of it falls back to the loud
// well-known write; first preference is always an isolated cell. It
// self-describes via UnsafeInfo for that fallback's warning. (Unlike the
// mock, claude's commands ride fileTemplateDelivery.DeliverCommands, which
// owns its own cleanup, so they are NOT the shared
// agent.ManagedCommandsDelivery.)
type commandsSurface struct {
	commands              []agent.CommandExport
	fs                    afero.Fs
	selfContainedCommands bool // mirrors SurfaceInputs.SelfContainedCommands; see DeliverCommands
}

// Present declares .claude/commands/. No flag: claude has no out-of-cwd
// redirect for slash commands.
func (s *commandsSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(relCommands).Build()
}

// Deliver writes .claude/commands/ beneath the advised project root via the
// reused file-template commands writer. selfContainedCommands rides along so a
// materialize target (a portable, self-contained tree) skips deduping against
// the delivering machine's ~/.claude/commands — see
// fileTemplateDelivery.DeliverCommands.
// reprise:accept-drift — the same deliberate three-line shape as mcpWriter.deliver and settingsSurface.deliver, for the reason recorded there: the shape IS the body, and a helper taking both the knob and the delivery as parameters is longer than what it replaces. Commands has no out-of-cwd variant, so the recipe needs no dir-taking split.
func (s *commandsSurface) Deliver(start present.Start) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: start.Paths().ProjectRoot.Host}, s.fs)
	d.selfContainedCommands = s.selfContainedCommands
	return d.DeliverCommands(s.commands)
}

// UnsafeInfo returns claude's commands identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go).
func (s *commandsSurface) UnsafeInfo() string { return "claude/commands" }

// newSkillsSurface builds claude's skills approach: an
// agent.ManagedSkillPackagesDelivery bound to WriteSkillFiles (skillfiles.go).
// The shared delivery type is reusable here (unlike commands) because claude's
// skill writer needs no home-dir dedup and no out-of-cwd form: no engine has
// an out-of-cwd flag for a skill package.
func newSkillsSurface(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
	fs = agent.GetFS(fs)
	return agent.NewManagedSkillPackagesDelivery("claude/skills", relSkills, in.Skills, func(dir string, skills []agent.SkillExport) error {
		return WriteSkillFiles(dir, skills, agent.WithCommandFS(fs))
	})
}

// Surfaces is claude's DECLARATION: per surface, every approach it can
// construct and which is the default — the ONE place claude's surface
// membership is stated. Construction is ROOT-FREE: every write, well-known
// and out-of-cwd alike, receives the run's advised roots when it runs, so
// nothing here binds a directory.
//
// context is a multi-approach surface — the native file (the DEFAULT,
// named explicitly rather than inferred from declaration order), the
// out-of-cwd system prompt, and the settings-carried hook (the shared
// implementation; claude registers it, it does not own it). settings is the
// other: the project file (the DEFAULT) and the record-backed write into the
// engine's private home (surfaces_hewrecord.go), which a binding selects by
// name. Every other surface has exactly one approach.
//
// A name here is known IF AND ONLY IF a constructor is registered under it, so
// "supported" and "constructible" cannot disagree — which is the whole reason
// this replaced a capability list beside a construction map. Every
// constructor takes the SHARED agent.SurfaceInputs directly rather than a
// local copy: two hand-maintained field-by-field mappers drift apart, as they
// once did on MCPCommandOverride. claude simply ignores the fields it has no
// use for (Fragments, AgentName).
var Surfaces = agent.Declaration{
	agent.SurfaceContext: agent.Presents("claude", agent.SurfaceContext, agent.ApproachUnsafeFile,
		agent.NativeContextFile("claude/context", ContextFileName, claudeContextWriter)).
		Or(ApproachSystemPrompt, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &systemPromptContext{content: in.Context, fs: agent.GetFS(fs)}
		}).
		Or(agent.ApproachHook, agent.HookCarriedContext),
	agent.SurfaceMCP: agent.Presents("claude", agent.SurfaceMCP, ApproachMCPConfig, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &mcpConfig{mcpWriter: newMCPWriter(in, fs)}
	}).Or(agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &mcpUnsafeFile{mcpWriter: newMCPWriter(in, fs)}
	}),
	agent.SurfaceSettings: agent.Presents("claude", agent.SurfaceSettings, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &settingsSurface{hooks: in.Hooks, manageStatusline: in.ManageStatusline, denyTools: in.DenyTools, fs: agent.GetFS(fs)}
	}).Or(ApproachHewRecord, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &settingsRecord{hooks: in.Hooks, manageStatusline: in.ManageStatusline, denyTools: in.DenyTools, fs: agent.GetFS(fs)}
	}),
	agent.SurfaceCommands: agent.Presents("claude", agent.SurfaceCommands, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &commandsSurface{commands: in.Commands, fs: agent.GetFS(fs), selfContainedCommands: in.SelfContainedCommands}
	}),
	agent.SurfaceSkills: agent.Presents("claude", agent.SurfaceSkills, agent.ApproachUnsafeFile, newSkillsSurface),
}

// Compile-time capability contracts. Every approach is an agent.Approach;
// system-prompt/MCP/settings ADDITIONALLY offer the out-of-cwd form — commands
// and skills have none, so a SHARED-cwd delivery of them always falls back to
// the loud well-known write (proved in surfaces_test.go).
var (
	_ agent.Approach   = (*systemPromptContext)(nil)
	_ agent.LaunchOnly = (*systemPromptContext)(nil)
	_ agent.Approach   = (*mcpConfig)(nil)
	_ agent.LaunchOnly = (*mcpConfig)(nil)
	_ agent.Approach   = (*mcpUnsafeFile)(nil)
	_ agent.Approach   = (*settingsSurface)(nil)
	_ agent.OutOfCwd   = (*settingsSurface)(nil)
	_ agent.Approach   = (*commandsSurface)(nil)
	_ placement        = dirPlacement{}
)

// pathed is the shape flagArgs reads off a delivered out-of-cwd form: the
// path its file actually landed at, "" when it did not.
type pathed interface{ Path() string }

// flagArgs returns the out-of-cwd launch flags for the surfaces this run
// actually delivered: --append-system-prompt-file, --mcp-config and --settings,
// each paired with the path its approach recorded. An approach reports ""
// when it delivered nothing (empty context/MCP/hooks, or context that fell
// back to the injection hook) and contributes no flag at all — claude must
// not be handed a flag naming a file that was never written. A nil resolved
// selection (before Setup) contributes nothing.
//
// Order is the resolved selection's (context, MCP, settings) because argv
// order is observable: a VARIADIC claude flag landing last before a
// positional swallows it (see buildArgs' prompt terminator).
func flagArgs(resolved *agent.ResolvedSelection) []string {
	if resolved == nil {
		return nil
	}
	var args []string
	// Resolved against NO roots: only the declared flag is read here, and the
	// declaration's flag does not depend on where anything lands. The flag
	// name is read from the approach's own presentation rather than from the
	// constant directly, which is what makes Present load-bearing: change a
	// declared flag and this argv changes with it.
	noRoots := present.New(present.OnHost(present.Paths{}))
	for _, ra := range resolved.Approaches() {
		p, ok := ra.Approach.(pathed)
		if !ok || p.Path() == "" {
			continue
		}
		announced := ra.Approach.Present(noRoots).Args
		if len(announced) == 0 {
			continue
		}
		args = append(args, announced[0], p.Path())
	}
	return args
}

// mcpServerNames lists, sorted, the bundle MCP servers the resolved MCP
// approach carries — the servers a plan-mode agent is allowed to reach (see
// permissionArgs). nil before Setup or when no MCP surface resolved.
func mcpServerNames(resolved *agent.ResolvedSelection) []string {
	if resolved == nil {
		return nil
	}
	for _, ra := range resolved.Approaches() {
		// Matched on the shared writer, not on one concrete approach: the
		// server set is the same whichever MCP approach delivered it, and
		// naming a single type here would silently return nil for the other.
		m, ok := ra.Approach.(interface {
			servers() map[string]wire.MCPServer
		})
		if !ok {
			continue
		}
		bundle := m.servers()
		out := make([]string, 0, len(bundle))
		for name := range bundle {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}
	return nil
}
