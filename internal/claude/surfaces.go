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
// place claude's surface membership is stated. Every approach WRAPS an
// existing claude writer verbatim — appendFlagDelivery (contextdelivery.go),
// fileTemplateDelivery (surfacedelivery.go), and the ContextWriter core
// WriteContext (claude.go). Context/MCP/settings ALSO carry an out-of-cwd
// form (agent.OutOfCwd) an engine launch flag consumes; buildArgs
// (claudecode.go) reads each such file's Path() after a SHARED-cwd delivery
// ran that form.
//
// Capability recap:
//
//	surface   | Delivery (well-known)     | also an out-of-cwd flag form?
//	----------|---------------------------|-----------------------------------------
//	context   | CLAUDE.md                 | ✅ --append-system-prompt-file <file> (the system-prompt approach)
//	MCP       | .mcp.json                 | ✅ --mcp-config <file> (--strict-mcp-config = replace)
//	settings  | .claude/settings.json     | ✅ --settings <file> (carries hooks)
//	commands  | .claude/commands/         | ❌ no out-of-cwd flag → loud native write when shared
//	skills    | .claude/skills/<name>/    | ❌ no out-of-cwd flag → loud native write when shared
//
// claude folds "settings + hooks" into ONE surface because claude's hooks live
// inside .claude/settings.json — there is no separate hooks file to deliver.

// ApproachSystemPrompt names claude's out-of-cwd framed context consumed via
// --append-system-prompt-file — semantically distinct from the native file
// (the content enters the system prompt, not project memory). It is claude's
// own name, declared here and nowhere shared: no other engine has it.
const ApproachSystemPrompt = "system-prompt"

// dirPlacement is a trivial agent.Placement whose Dir() returns a fixed
// directory. It adapts a root read from the advised Start into the Placement
// the reused writers (fileTemplateDelivery, appendFlagDelivery) construct
// against — the project root for the well-known Delivery, the Scratch root
// for the out-of-cwd forms; both arrive at call time, never at construction.
type dirPlacement struct{ dir string }

// Dir returns the fixed directory this placement wraps.
func (p dirPlacement) Dir() string { return p.dir }

// claudeContextWriter is the ContextWriter the native-file context approach
// merges through — the same core WriteContext (claude.go) every CLAUDE.md
// write uses.
func claudeContextWriter(fs afero.Fs) agent.ContextWriter { return &ClaudeCodeHookWriter{FS: fs} }

// systemPromptContext is claude's system-prompt context approach.
//
// Its out-of-cwd form (DeliverIsolated) writes the framed <hash>.sysprompt.md
// beneath the advised Scratch root via the existing appendFlagDelivery and
// exposes its path (Path) for --append-system-prompt-file; a SHARED-cwd
// launch runs that form, race-free. It is LaunchOnly: at rest there is no
// argv sink for the flag, so DeliverUnder refuses it.
//
// Its well-known Deliver writes CLAUDE.md — the SAME write the native-file
// approach performs. That is what an ISOLATED launch cell runs for it, and it
// is preserved exactly as it was: whether a system-prompt pin on a worktree or
// container launch should instead land the scratch file there (the cell's
// Scratch root is the private working dir itself) is an open question that
// was deliberately NOT decided in the refactor that made this its own type.
type systemPromptContext struct {
	content string
	fs      afero.Fs
	path    string // set by DeliverIsolated: the out-of-cwd framed context file
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
	return start.UnderScratch("").AnnounceFlag(flagAppendSystemFile).Build()
}

// Deliver is the well-known CLAUDE.md write (see the type doc for why).
func (s *systemPromptContext) Deliver(start present.Start) (agent.Delivered, error) {
	return agent.DeliverManagedContext(claudeContextWriter(s.fs), start.Paths().ProjectRoot.Host, s.content)
}

// DeliverIsolated writes the framed context file through the reused
// appendFlagDelivery beneath the advised Scratch root; Path then exposes it.
// A FAILED write leaves Path "" (the writer's own contract), for the same
// reason mcpSurface's does: no flag may name a file that was not written.
func (s *systemPromptContext) DeliverIsolated(start present.Start) (agent.Delivered, error) {
	d := newAppendFlagDelivery(dirPlacement{dir: start.Paths().Scratch.Host}, s.fs)
	handle, err := d.DeliverContext(s.content)
	s.path = d.Path()
	return handle, err
}

// Path returns the framed <hash>.sysprompt.md written by DeliverIsolated (for
// --append-system-prompt-file), or "" whenever no file stands behind it: before
// delivery, for empty context, and after a FAILED delivery.
func (s *systemPromptContext) Path() string { return s.path }

// mcpSurface is claude's MCP approach.
//
// Deliver (well-known) writes .mcp.json into the project root via the reused
// fileTemplateDelivery.DeliverMCP. DeliverIsolated writes the same merged
// .mcp.json beneath Scratch and exposes its path (Path) for --mcp-config
// <file> (paired with --strict-mcp-config, which replaces the project
// .mcp.json rather than merging — a buildArgs concern).
type mcpSurface struct {
	bundle          map[string]wire.MCPServer
	fs              afero.Fs
	path            string // set by DeliverIsolated: the out-of-cwd .mcp.json
	commandOverride string // see SurfaceInputs.MCPCommandOverride
}

// Present declares .mcp.json plus the --mcp-config flag its out-of-cwd form
// is announced with.
func (s *mcpSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(MCPFileName).AnnounceFlag(flagMCPConfig).Build()
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

// Deliver writes .mcp.json beneath the advised project root via the reused
// file-template MCP writer.
func (s *mcpSurface) Deliver(start present.Start) (agent.Delivered, error) {
	return s.deliver(start.Paths().ProjectRoot.Host)
}

// DeliverIsolated writes the merged .mcp.json beneath the advised Scratch root
// and records its path for --mcp-config. A FAILED write clears that path:
// Path() promises "" for a file that does not exist, and flagArgs must never
// hand claude --mcp-config naming one.
func (s *mcpSurface) DeliverIsolated(start present.Start) (agent.Delivered, error) {
	handle, err := s.deliver(start.Paths().Scratch.Host)
	if err != nil {
		s.path = ""
		return nil, err
	}
	// The recorded path comes from the DECLARED leaf, not from a second
	// hand-written join: it is what --mcp-config is pointed at, so a wrong rel
	// path cannot pass unnoticed.
	s.path = start.UnderScratch(MCPFileName).Build().HostPath
	return handle, nil
}

// Path returns the out-of-cwd .mcp.json written by DeliverIsolated (for
// --mcp-config <file>), or "" before delivery and after a FAILED one.
func (s *mcpSurface) Path() string { return s.path }

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
	// (below), mirroring mcpSurface.commandOverride, so DeliverSettings's
	// signature stays exactly agent.SettingsDelivery — no cross-module
	// interface change for an engine-specific extra.
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
// mcpSurface.deliver for the shape): build the reused file-template writer
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
// that path, for the same reason mcpSurface's does: no --settings flag may
// name a file that was not written.
func (s *settingsSurface) DeliverIsolated(start present.Start) (agent.Delivered, error) {
	handle, err := s.deliver(start.Paths().Scratch.Host)
	if err != nil {
		s.path = ""
		return nil, err
	}
	// Declared, not re-joined — see mcpSurface.DeliverIsolated.
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
// reprise:accept-drift — the same deliberate three-line shape as mcpSurface.deliver and settingsSurface.deliver, for the reason recorded there: the shape IS the body, and a helper taking both the knob and the delivery as parameters is longer than what it replaces. Commands has no out-of-cwd variant, so the recipe needs no dir-taking split.
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
// context is the one multi-approach surface — the native file (the DEFAULT,
// named explicitly rather than inferred from declaration order), the
// out-of-cwd system prompt, and the settings-carried hook (the shared
// implementation; claude registers it, it does not own it). Every other
// surface has exactly one approach.
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
	agent.SurfaceMCP: agent.Presents("claude", agent.SurfaceMCP, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &mcpSurface{bundle: in.BundleMCP, fs: agent.GetFS(fs), commandOverride: in.MCPCommandOverride}
	}),
	agent.SurfaceSettings: agent.Presents("claude", agent.SurfaceSettings, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &settingsSurface{hooks: in.Hooks, manageStatusline: in.ManageStatusline, denyTools: in.DenyTools, fs: agent.GetFS(fs)}
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
	_ agent.OutOfCwd   = (*systemPromptContext)(nil)
	_ agent.LaunchOnly = (*systemPromptContext)(nil)
	_ agent.Approach   = (*mcpSurface)(nil)
	_ agent.OutOfCwd   = (*mcpSurface)(nil)
	_ agent.Approach   = (*settingsSurface)(nil)
	_ agent.OutOfCwd   = (*settingsSurface)(nil)
	_ agent.Approach   = (*commandsSurface)(nil)
	_ agent.Placement  = dirPlacement{}
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
		m, ok := ra.Approach.(*mcpSurface)
		if !ok {
			continue
		}
		out := make([]string, 0, len(m.bundle))
		for name := range m.bundle {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}
	return nil
}
