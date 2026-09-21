package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// This file is claude's DEFINITION on the engine port: the one typed
// approach per surface kind the engine carries, the dynamic approach it
// provides, its modes and their argv grammars (a projection of the L1
// EngineCLI declarations), its permission facts and its export schema. It
// is the example of an engine with BOTH halves of delivery — every
// non-preface item statically through its typed approaches, the preface
// items dynamically on the session's MCP endpoint through the dynamic
// approach it provides. Which item goes where is engine.Base.Delegate's
// decision, not this package's.
//
// Each typed approach still carries its runtime FORMS (agent.Forms): the
// named constructors today's launch path builds writers from by selection.
// They hang off the typed approach so claude keeps one table; the delivery
// seam that constructs by name retires them.
//
// Hooks IS a delivered surface here, not a fold into settings: claude's
// native form for a hook registration is a section of .claude/settings.json,
// so the hooks approach writes through the same settings writer — the
// definition says so through the approach type, and DeliverHooks is real.

// Claude is the engine KIND: the engine root embedded, engine-specific logic
// as methods. Built once by Build; immutable. transcripts are the readers
// of claude's own store the composition root handed in: a transcript
// adapter's values, never this package's import.
type Claude struct {
	engine.Base
	transcripts []engine.TranscriptReader
	// open, when set, replaces the spawned `claude` process as the
	// stream-json driver's I/O seam (the hook the driver's tests use to run
	// a turn against in-memory pipes); now, when set, replaces time.Now as
	// the clock stamping chat entries that arrive without a timestamp.
	open chatTransportFunc
	now  func() time.Time
}

// Option adjusts the kind before Validate.
type Option func(*Claude)

// WithTranscripts hands the kind the readers of its own transcript store.
func WithTranscripts(readers ...engine.TranscriptReader) Option {
	return func(c *Claude) { c.transcripts = append(c.transcripts, readers...) }
}

// Build is THE CONSTRUCTOR: the one place claude's declaration is assembled
// and the one place an incoherent one is refused, by engine.Base.Validate.
// The shape is the plain constructor: the literal, options, then Validate
// once.
func Build(opts ...Option) (engine.Engine, error) {
	home := []present.RootKind{present.RootSessionHome}
	// Every static surface roots under the session home FIRST — the engine's
	// config home for the session (ConfigDirEnv), where claude reads its
	// user-level settings, commands and skills — and offers the project root
	// SECOND, reached only when the binding's `roots:` selects it. No default
	// names the project or the user's real home (ruled 2026-09-21).
	shared := []present.RootKind{present.RootSessionHome, present.RootProjectRoot}
	var cli []engine.CLIGrammar
	for _, c := range ClaudeEngineCLIs() {
		cli = append(cli, agent.GrammarOf(c))
	}
	d := engine.Definition{
		Name:         EngineName,
		Distribution: engine.DistributionDefault,
		Modes:        []engine.Mode{engine.Interactive, engine.Structured},
		Permissions: engine.PermissionFacts{
			Native:       []engine.PermissionMode{engine.PermissionDefault, engine.PermissionPlan, engine.PermissionAcceptEdits, engine.PermissionBypass},
			ReadOnlyPlan: true, // --permission-mode plan is read-only
			// The host stopgap: blanket auto-approval on the bare host, until
			// the approval broker delivers prompts to a human.
			HostDefault:       engine.PermissionBypass,
			HostDefaultReason: "permissions bypassed on the host (claude-code stopgap)",
		},
		Context:  &contextApproach{traits{present.Traits{Roots: shared, Channel: present.ChannelArgv}}},
		MCP:      &mcpApproach{traits{present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true}}},
		Settings: &settingsApproach{traits{present.Traits{Roots: shared, Channel: present.ChannelFile}}},
		Hooks:    &hooksApproach{traits{present.Traits{Roots: shared, Channel: present.ChannelFile}}},
		Commands: &commandsApproach{traits{present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true}}},
		Skills:   &skillsApproach{traits{present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true}}},
		// The dynamic half, PROVIDED: the session endpoint as an entry in the
		// MCP file.
		Dynamic:      &sessionEndpoint{traits{present.Traits{Roots: home, Channel: present.ChannelFile}}},
		CLI:          cli,
		ModelAliases: map[string]string{},
		ExportSchema: ExportSchema,
	}
	c := Claude{Base: engine.Base{Definition: d}}
	for _, o := range opts {
		o(&c)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := c.Home().Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

var _ engine.Engine = Claude{}

// Declaration is the named-form table today's launch path constructs
// writers from, DERIVED from the Definition: each typed approach's Forms
// under its kind. The Definition literal is constant and conformance holds
// it valid, so a refusal here is a programming error and panics.
func Declaration() agent.Declaration {
	e, err := Build()
	if err != nil {
		panic(err)
	}
	return agent.DeclarationOf(e.Root().Surfaces())
}

// traits is the declared facts every typed approach here carries.
type traits struct{ t present.Traits }

func (a traits) Traits() present.Traits { return a.t }

// errRoot is the refusal for a root the approach does not offer: the plan
// selected one outside Traits().Roots.
func errRoot(name string, root present.RootKind) error {
	return fmt.Errorf("claude/%s: root %v is not one this approach offers", name, root)
}

// delivered adapts a runtime form's write into the port's Delivered: the
// presentation the form composes, the path it recorded (when it has one)
// and the cleanup handle as Undo.
func delivered(a agent.Approach, start present.Start, h agent.Delivered) present.Delivered {
	out := present.Delivered{Presented: a.Present(start)}
	if p, ok := a.(pathed); ok && p.Path() != "" {
		out.Wrote = []string{p.Path()}
	}
	if h != nil {
		out.Undo = func(afero.Fs) error { return h.Cleanup() }
	}
	return out
}

// contextApproach is claude's context surface: the framed system prompt,
// announced on --append-system-prompt-file, under the session home; at the
// project root (a materialize, or a binding that selects the shared root)
// the well-known CLAUDE.md, the assembled context appended to whatever the
// file already holds. The ownership record owns the write and restores the
// prior bytes on removal — there is no marker section to parse.
type contextApproach struct{ traits }

func (*contextApproach) Name() string { return ApproachSystemPrompt }
func (*contextApproach) Forms() agent.Presentations {
	return agent.Presents(EngineName, agent.SurfaceContext, agent.ApproachUnsafeFile,
		agent.NativeContextFile("claude/context", ContextFileName, claudeContextWriter)).
		Or(ApproachSystemPrompt, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
			return &systemPromptContext{content: in.Context, fs: agent.GetFS(fs)}
		}).
		Or(agent.ApproachHook, agent.HookCarriedContext)
}
func (a *contextApproach) DeliverContext(start present.Start, root present.RootKind, in engine.ContextInputs, fs afero.Fs) (present.Delivered, error) {
	switch root {
	case present.RootProjectRoot:
		return appendContextFile(start.UnderProjectRoot(ContextFileName).Build(), in.Text, agent.GetFS(fs))
	case present.RootSessionHome:
	default:
		return present.Delivered{}, errRoot(a.Name(), root)
	}
	s := &systemPromptContext{content: string(in.Text), fs: agent.GetFS(fs)}
	h, err := s.Deliver(start)
	if err != nil {
		return present.Delivered{}, err
	}
	return delivered(s, start, h), nil
}

// appendContextFile writes the context after the file's current bytes: the
// user's CLAUDE.md is theirs, and the record owns what was appended.
func appendContextFile(p present.Presentation, text []byte, fs afero.Fs) (present.Delivered, error) {
	// A CLAUDE.md ctxloom creates is owner-only; a user's keeps its mode.
	if err := iox.AppendSection(fs, p.HostPath, text, 0o600); err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Wrote: []string{p.HostPath}, Undo: func(fs afero.Fs) error { return fs.Remove(p.HostPath) }}, nil
}

// mcpApproach is claude's MCP surface: .mcp.json under the session home
// (announced on --mcp-config) or, when the plan selects the shared root, the
// project's own well-known .mcp.json.
type mcpApproach struct{ traits }

func (*mcpApproach) Name() string { return ApproachMCPConfig }
func (*mcpApproach) Forms() agent.Presentations {
	return agent.Presents(EngineName, agent.SurfaceMCP, ApproachMCPConfig, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &mcpConfig{mcpWriter: newMCPWriter(in, fs)}
	}).Or(agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &mcpUnsafeFile{mcpWriter: newMCPWriter(in, fs)}
	})
}
func (a *mcpApproach) DeliverMCP(start present.Start, root present.RootKind, in engine.MCPInputs, fs afero.Fs) (present.Delivered, error) {
	w := mcpWriter{bundle: in.Servers, fs: agent.GetFS(fs)}
	var form agent.Approach
	switch root {
	case present.RootSessionHome:
		form = &mcpConfig{mcpWriter: w}
	case present.RootProjectRoot:
		form = &mcpUnsafeFile{mcpWriter: w}
	default:
		return present.Delivered{}, errRoot(a.Name(), root)
	}
	h, err := form.Deliver(start)
	if err != nil {
		return present.Delivered{}, err
	}
	return delivered(form, start, h), nil
}

// deliverSettingsFile writes ctxloom's entries INTO claude's settings file
// for the root — the well-known .claude/settings.json under the project
// root, or settings.json under the session home — additively: hooks are
// added, the statusline set when asked for, the deny list merged, and
// nothing already there is removed. Removal is the ownership record's
// (the static writer reconciles a delivery from clean), which is what
// lets the settings and hooks kinds share one file: each adds its own
// entries and neither undoes the other's. Settings and hooks share it
// because claude keeps both in the one file.
func deliverSettingsFile(name string, start present.Start, root present.RootKind, hooks *wire.HooksConfig, statusline bool, deny []string, fs afero.Fs) (present.Delivered, error) {
	var p present.Presentation
	switch root {
	case present.RootProjectRoot:
		p = start.UnderProjectRoot(filepath.Join(ConfigDirName, SettingsFileName)).Build()
	case present.RootSessionHome:
		if err := agent.EngineHomeRooted(start); err != nil {
			return present.Delivered{}, err
		}
		p = start.UnderEngineHome(SettingsFileName).Build()
	default:
		return present.Delivered{}, errRoot(name, root)
	}
	w := &ClaudeCodeHookWriter{FS: agent.GetFS(fs)}
	if err := w.addToSettingsFile(p.HostPath, hooks, statusline, deny); err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Wrote: []string{p.HostPath}}, nil
}

// settingsApproach is claude's settings surface: statusline and the deny
// list, in .claude/settings.json.
type settingsApproach struct{ traits }

func (*settingsApproach) Name() string { return "settings" }
func (*settingsApproach) Forms() agent.Presentations {
	return agent.Presents(EngineName, agent.SurfaceSettings, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &settingsSurface{hooks: in.Hooks, manageStatusline: in.ManageStatusline, denyTools: in.DenyTools, reporter: in.Reporter, fs: agent.GetFS(fs)}
	}).Or(ApproachHewRecord, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &settingsRecord{hooks: in.Hooks, manageStatusline: in.ManageStatusline, denyTools: in.DenyTools, fs: agent.GetFS(fs)}
	})
}
func (a *settingsApproach) DeliverSettings(start present.Start, root present.RootKind, in engine.SettingsInputs, fs afero.Fs) (present.Delivered, error) {
	return deliverSettingsFile(a.Name(), start, root, nil, in.Statusline, in.DenyTools, fs)
}

// hooksApproach is claude's hooks surface: the hook registrations, written
// as the hooks section of the same settings.json the settings approach
// writes — the native form. Each typed Deliver adds its own entries to
// that file and removes nothing (deliverSettingsFile), so the two kinds
// share it without either undoing the other.
type hooksApproach struct{ traits }

func (*hooksApproach) Name() string { return "settings-hooks" }
func (a *hooksApproach) DeliverHooks(start present.Start, root present.RootKind, in engine.HooksInputs, fs afero.Fs) (present.Delivered, error) {
	return deliverSettingsFile(a.Name(), start, root, &wire.HooksConfig{Unified: in.Hooks}, false, nil, fs)
}

// commandsApproach is claude's commands surface: <config dir>/commands/
// under the session home (the user-level directory claude loads alongside a
// project's), or .claude/commands/ under the project root when the binding
// selects it; a command's help text and metadata arrive already decoded
// from its block (Claude.Exports) and become the slash-command frontmatter.
type commandsApproach struct{ traits }

func (*commandsApproach) Name() string { return "commands-dir" }
func (*commandsApproach) Forms() agent.Presentations {
	return agent.Presents(EngineName, agent.SurfaceCommands, agent.ApproachUnsafeFile, func(in agent.SurfaceInputs, fs afero.Fs) agent.Approach {
		return &commandsSurface{commands: in.Commands, fs: agent.GetFS(fs), reporter: in.Reporter, selfContainedCommands: in.SelfContainedCommands}
	})
}
func (a *commandsApproach) DeliverCommands(start present.Start, root present.RootKind, in engine.CommandsInputs, fs afero.Fs) (present.Delivered, error) {
	cmds := make([]agent.CommandExport, 0, len(in.Commands))
	for _, c := range in.Commands {
		cmds = append(cmds, agent.CommandExport{
			Name: c.Name, Content: string(c.Body), Enabled: c.Enabled,
			Description: c.Description, ArgumentHint: c.ArgumentHint, AllowedTools: c.AllowedTools, Model: c.Model,
		})
	}
	switch root {
	case present.RootSessionHome:
		// The session's config dir is its own instance: nothing in the
		// user's real ~/.claude/commands is deduped against, and a run with
		// no engine home advised is refused rather than served from there.
		if err := privateRooted(start); err != nil {
			return present.Delivered{}, err
		}
		p := underPrivateRoot(start, CommandsDirName).Build()
		if err := writeCommandDir(agent.GetFS(fs), p.HostPath, cmds); err != nil {
			return present.Delivered{}, err
		}
		return present.Delivered{Presented: p, Wrote: []string{p.HostPath}}, nil
	case present.RootProjectRoot:
	default:
		return present.Delivered{}, errRoot(a.Name(), root)
	}
	// The plan's commands land as given: a copy in the materializing
	// host's own ~/.claude/commands is no reason to withhold one from a
	// tree that will be read elsewhere.
	form := &commandsSurface{commands: cmds, fs: agent.GetFS(fs), selfContainedCommands: true}
	h, err := form.Deliver(start)
	if err != nil {
		return present.Delivered{}, err
	}
	return delivered(form, start, h), nil
}

// skillsApproach is claude's skills surface: <config dir>/skills/<name>/
// under the session home (the user-level directory claude loads alongside a
// project's), or .claude/skills/<name>/ under the project root when the
// binding selects it.
type skillsApproach struct{ traits }

func (*skillsApproach) Name() string { return "skills-dir" }
func (*skillsApproach) Forms() agent.Presentations {
	return agent.Presents(EngineName, agent.SurfaceSkills, agent.ApproachUnsafeFile, newSkillsSurface)
}
func (a *skillsApproach) DeliverSkills(start present.Start, root present.RootKind, in engine.SkillsInputs, fs afero.Fs) (present.Delivered, error) {
	skills := make([]agent.SkillExport, 0, len(in.Skills))
	for _, s := range in.Skills {
		e := agent.SkillExport{Name: s.Name, Description: s.Description, Enabled: s.Enabled}
		for _, f := range s.Files {
			mode := os.FileMode(f.Mode)
			if mode == 0 {
				mode = 0o644
			}
			e.Files = append(e.Files, agent.PackageFile{RelPath: f.Path, Content: f.Bytes, Mode: mode})
		}
		skills = append(skills, e)
	}
	switch root {
	case present.RootSessionHome:
		if err := privateRooted(start); err != nil {
			return present.Delivered{}, err
		}
		p := underPrivateRoot(start, SkillsDirName).Build()
		if err := agent.WriteManagedSkillPackages(agent.GetFS(fs), p.HostPath, acceptedSkills(skills)); err != nil {
			return present.Delivered{}, err
		}
		return present.Delivered{Presented: p, Wrote: []string{p.HostPath}}, nil
	case present.RootProjectRoot:
	default:
		return present.Delivered{}, errRoot(a.Name(), root)
	}
	form := newSkillsSurface(agent.SurfaceInputs{Skills: skills}, fs)
	h, err := form.Deliver(start)
	if err != nil {
		return present.Delivered{}, err
	}
	return delivered(form, start, h), nil
}

// sessionEndpoint is the dynamic approach claude PROVIDES: the session's MCP
// endpoint as a URL entry in .mcp.json with the bearer as a header.
type sessionEndpoint struct{ traits }

func (*sessionEndpoint) Name() string                                 { return "session-endpoint" }
func (*sessionEndpoint) Endpoint(ep sessions.Endpoint) wire.MCPServer { return engine.BearerEntry(ep) }

// Compile-time contracts: each typed approach fills its kind's field and,
// where today's launch path constructs by name, carries its runtime forms.
var (
	_ engine.ContextApproach  = (*contextApproach)(nil)
	_ engine.MCPApproach      = (*mcpApproach)(nil)
	_ engine.SettingsApproach = (*settingsApproach)(nil)
	_ engine.HooksApproach    = (*hooksApproach)(nil)
	_ engine.CommandsApproach = (*commandsApproach)(nil)
	_ engine.SkillsApproach   = (*skillsApproach)(nil)
	_ engine.DynamicApproach  = (*sessionEndpoint)(nil)
	_ agent.Forms             = (*contextApproach)(nil)
	_ agent.Forms             = (*mcpApproach)(nil)
	_ agent.Forms             = (*settingsApproach)(nil)
	_ agent.Forms             = (*commandsApproach)(nil)
	_ agent.Forms             = (*skillsApproach)(nil)
)
