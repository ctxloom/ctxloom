package claude

import (
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file holds claude's runtime FORMS on the surface-delivery seam
// (internal/core/agent/cells.go, declaration.go): each form is a value
// implementing agent.Approach, constructed by name from the Forms the typed
// approaches in definition.go carry. claude's surface membership itself is
// stated ONCE, by the typed fields of its engine.Definition (Build); the
// named table this seam reads is derived from it (Declaration). Every form
// here WRAPS an existing claude writer verbatim — appendFlagDelivery
// (contextdelivery.go), fileTemplateDelivery (surfacedelivery.go), and the
// ContextWriter core WriteContext (claude.go); the record-backed settings
// form, which writes through confpatch instead, lives in
// surfaces_hewrecord.go. buildArgs (claudecode.go) reads each
// flag-announced form's Path() after delivery, on every cell.
//
// A surface with TWO forms names both: which one runs is the caller's
// selection, never a conversion applied underneath it. Where each
// form's bytes land is stated on the form (Present); which forms a kind
// has is stated on its typed approach (Forms).
//
// Hooks are delivered through the settings forms: claude keeps hook
// registrations inside .claude/settings.json, so the hooks approach
// (definition.go) writes through the same writer as settings.

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
// the session home for the out-of-cwd forms; all arrive at call
// time, never at construction.
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
// It is the run's SESSION HOME, the relocated engine home: configuration is a
// property of the run, mounted into the container and inited against, and
// the home is resolved off the agent binding alone — orthogonal to the cell,
// so every cell kind reaches the same root.
//
// Only a binding that relocates the home (engine_home: session) advises this
// root at all. A run without one is REFUSED by every approach beneath it
// (privateRooted) rather than served from the user's real home, which is
// shared across every session and exactly the file these approaches exist to
// stay out of.
func privateRoot(start present.Start) present.Root { return start.Paths().SessionHome }

// underPrivateRoot roots a PRESENTATION at rel beneath the same root
// privateRoot names. It sits here, adjacent to privateRoot and nowhere else,
// because the two must move together: the presenter states where the bytes go
// and Deliver puts them there, so a flip that changed one and not the other
// would announce a path nothing was written to. They are checked against each
// other by TestSurfaces_PresentedPathIsWhereTheApproachWrites.
func underPrivateRoot(start present.Start, rel string) present.Rooted {
	return start.UnderSessionHome(rel)
}

// privateRooted is the entry refusal for an approach that lands beneath
// privateRoot: the seam's third member, kept adjacent for the same reason as
// underPrivateRoot. The refusal is root-specific — each advised root has its
// own sentinel naming its own remedy — so a flip that moved the placement and
// left the check on the old root would refuse runs that HAVE the new root and
// serve runs that lack it, with a bare relative path.
func privateRooted(start present.Start) error { return agent.SessionHomeRooted(start) }

// claudeContextWriter is the ContextWriter the native-file context approach
// merges through — the same core WriteContext (claude.go) every CLAUDE.md
// write uses.
func claudeContextWriter(fs afero.Fs) agent.ContextWriter { return &ClaudeCodeHookWriter{FS: fs} }

// newMCPWriter builds the .mcp.json recipe both MCP approaches embed, from the
// one set of run inputs. It exists so the two constructors cannot come apart
// on a field.
func newMCPWriter(in agent.SurfaceInputs, fs afero.Fs) mcpWriter {
	return mcpWriter{bundle: in.BundleMCP, fs: agent.GetFS(fs)}
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
// gets ErrUnrootedSessionHome and writes nothing.
type systemPromptContext struct {
	content string
	fs      afero.Fs
	path    string // set by Deliver: the framed context file under the private root
}

// LaunchOnly marks the approach as refused at rest.
func (*systemPromptContext) LaunchOnly() {}

// Present declares the out-of-cwd form's flag, naming the basename Deliver
// already wrote at Path() — appendFlagDelivery names the file
// <hash>.sysprompt.md where <hash> is a sha256 prefix over the FRAMED BYTES,
// so the leaf is unknowable until Deliver has run, and Present reads it back
// from Path() rather than recomputing it. filepath.Base(s.path) rather than
// s.path itself, because Path() is a HOST path and underPrivateRoot performs
// the same host/engine root mapping every other surface's Present goes
// through (container-rootless/rootful differ there); only the leaf is
// specific to this file, the root is not.
//
// s.path == "" is Path()'s own no-file contract — before delivery, for empty
// context, and after a FAILED delivery — and every one of those is a case
// with no written file behind it. Announcing the bare private root as the
// flag's value would violate Deliver's invariant that no flag may name a
// file that was not written, so this presents nothing at all: an unrooted
// Presentation{} for the caller to skip, the same shape hookCarriedContext
// uses for "nothing to present" (approaches_generic.go).
func (s *systemPromptContext) Present(start present.Start) present.Presentation {
	if s.path == "" {
		return present.Presentation{}
	}
	return underPrivateRoot(start, filepath.Base(s.path)).AnnounceFlag(flagAppendSystemFile).Build()
}

// Deliver writes the framed context file through the reused appendFlagDelivery
// beneath the advised private root; Path then exposes it for
// --append-system-prompt-file. This is the approach's ONLY form, so every cell
// reaches it — an isolated launch that selected system-prompt now gets the
// system prompt.
//
// An unresolved private root REFUSES (ErrUnrootedSessionHome) rather than writing
// the well-known file instead; see the type doc. A FAILED write leaves Path ""
// (the writer's own contract): no flag may name a file that was not written.
func (s *systemPromptContext) Deliver(start present.Start) (agent.Delivered, error) {
	if err := privateRooted(start); err != nil {
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
	bundle map[string]wire.MCPServer
	fs     afero.Fs
}

// deliver writes the merged .mcp.json into dir.
func (w mcpWriter) deliver(dir string) (agent.Delivered, error) {
	return newFileTemplateDelivery(dirPlacement{dir: dir}, w.fs).DeliverMCP(w.bundle)
}

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
// (ErrUnrootedSessionHome) rather than falling back to the project file — the
// fallback IS the defect. A FAILED write clears the path: Path() promises ""
// for a file that does not exist, and the delivered presentation must never
// hand claude --mcp-config naming one.
//
// The file is MATERIALIZED even when the run registers no servers, because
// Present announces --mcp-config unconditionally and claude REFUSES to start
// against a path that does not exist ("Invalid MCP configuration: MCP config
// file not found"), exiting before it emits anything — which reaches the
// caller as an empty answer and gets reported as a dead or unauthenticated
// engine. The merge alone does not guarantee the file: an empty server set
// records no edits, so confpatch writes nothing and reports success. Under
// the PRIVATE session root the file is ctxloom's own to create, so the flag
// is kept honest by construction. mcpUnsafeFile deliberately does NOT do
// this — its target is the user's project .mcp.json, which ctxloom does not
// conjure just to satisfy a flag.
func (s *mcpConfig) Deliver(start present.Start) (agent.Delivered, error) {
	if err := privateRooted(start); err != nil {
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
	p := underPrivateRoot(start, MCPFileName).Build().HostPath
	if err := materializeEmptyMCPConfig(agent.GetFS(s.fs), p); err != nil {
		s.path = ""
		return nil, err
	}
	s.path = p
	return handle, nil
}

// materializeEmptyMCPConfig writes an empty {"mcpServers":{}} document at
// path when the merge left no file there. It never touches one that exists:
// the merged document is the authority whenever there is one.
func materializeEmptyMCPConfig(fs afero.Fs, path string) error {
	switch ok, err := afero.Exists(fs, path); {
	case err != nil:
		return err
	case ok:
		return nil
	}
	doc, err := agent.MarshalChatMCPConfig(nil)
	if err != nil {
		return err
	}
	// safefs.WriteFile, not afero.WriteFile: 0o600 must land EXACTLY,
	// and a torn write here is a config file claude refuses to start against.
	return safefs.WriteFile(fs, path, doc, 0o600)
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
// the reused fileTemplateDelivery.DeliverSettings.
type settingsSurface struct {
	hooks            *wire.HooksConfig
	manageStatusline bool
	reporter         report.Sink // SurfaceInputs.Reporter, forwarded to the settings writer
	// denyTools is the resolved deny_tools union (SurfaceInputs.DenyTools) —
	// per-tool identifiers (e.g. "Task") this run's settings.json denies via
	// permissions.deny. Threaded to fileTemplateDelivery as a RECEIVER field
	// (below) so DeliverSettings's signature stays unchanged for an
	// engine-specific extra.
	denyTools []string
	fs        afero.Fs
}

// Present declares .claude/settings.json (hooks + statusline) and names it on
// --settings.
func (s *settingsSurface) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(relSettings).AnnounceFlag(flagSettings).Build()
}

// deliver is the .claude/settings.json recipe (see mcpWriter.deliver for the
// shape): build the reused file-template writer
// against dir, thread the resolved deny_tools union onto it, and write the
// settings JSON including hooks and the statusline policy.
func (s *settingsSurface) deliver(dir string) (agent.Delivered, error) {
	d := newFileTemplateDelivery(dirPlacement{dir: dir}, s.fs)
	d.reporter = s.reporter
	d.denyTools = s.denyTools
	return d.DeliverSettings(s.hooks, s.manageStatusline)
}

// Deliver writes .claude/settings.json beneath the advised project root via
// the reused file-template settings writer.
func (s *settingsSurface) Deliver(start present.Start) (agent.Delivered, error) {
	return s.deliver(start.Paths().ProjectRoot.Host)
}

// commandsSurface is claude's commands approach: the slash-command exports
// under .claude/commands/. claude has no out-of-cwd flag for slash-commands,
// so a SHARED-cwd delivery of it is the loud well-known write; first preference is always an isolated cell. It
// self-describes via UnsafeInfo for that fallback's warning. (Unlike the
// mock, claude's commands ride fileTemplateDelivery.DeliverCommands, which
// owns its own cleanup, so they are NOT the shared
// agent.ManagedCommandsDelivery.)
type commandsSurface struct {
	commands              []agent.CommandExport
	fs                    afero.Fs
	reporter              report.Sink // SurfaceInputs.Reporter, forwarded to the writer
	selfContainedCommands bool        // mirrors SurfaceInputs.SelfContainedCommands; see DeliverCommands
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
	d.reporter = s.reporter
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
		return WriteSkillFiles(dir, skills, agent.WithCommandFS(fs), agent.WithReporter(in.Reporter))
	})
}

// Compile-time capability contracts. Every approach is an agent.Approach.
// The private-root approaches are LaunchOnly; commands and skills have no
// out-of-cwd form at all, so a SHARED-cwd delivery of them is always the loud
// well-known write (proved in surfaces_test.go).
var (
	_ agent.Approach   = (*systemPromptContext)(nil)
	_ agent.LaunchOnly = (*systemPromptContext)(nil)
	_ agent.Approach   = (*mcpConfig)(nil)
	_ agent.LaunchOnly = (*mcpConfig)(nil)
	_ agent.Approach   = (*mcpUnsafeFile)(nil)
	_ agent.Approach   = (*settingsSurface)(nil)
	_ agent.Approach   = (*commandsSurface)(nil)
	_ placement        = dirPlacement{}
)
