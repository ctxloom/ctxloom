package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	open kit.TransportFunc
	now  func() time.Time
}

// Option adjusts the kind before Validate.
type Option func(*Claude)

// WithTranscripts hands the kind the readers of its own transcript store.
func WithTranscripts(readers ...engine.TranscriptReader) Option {
	return func(c *Claude) { c.transcripts = append(c.transcripts, readers...) }
}

// WithVersion hands the kind the reading of `claude --version`: the parse is
// the version adapter's, so the composition root supplies it beside the
// readers rather than this package importing an adapter.
func WithVersion(v engine.VersionCommand) Option {
	return func(c *Claude) { c.Version = v }
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
		Context:      &contextApproach{approach(ApproachSystemPrompt, present.Traits{Roots: shared, Channel: present.ChannelArgv})},
		MCP:          &mcpApproach{approach(ApproachMCPConfig, present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true})},
		Settings:     &settingsApproach{approach("settings", present.Traits{Roots: shared, Channel: present.ChannelFile})},
		Hooks:        &hooksApproach{approach("settings-hooks", present.Traits{Roots: shared, Channel: present.ChannelFile})},
		Commands:     &commandsApproach{approach("commands-dir", present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true})},
		Skills:       &skillsApproach{approach("skills-dir", present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true})},
		// The dynamic half, PROVIDED: the session endpoint as an entry in the
		// MCP file.
		Dynamic:      &sessionEndpoint{approach("session-endpoint", present.Traits{Roots: home, Channel: present.ChannelFile})},
		CLI:          cli,
		ModelAliases: map[string]string{},
		ExportSchema: ExportSchema,
		// Reviewed onto StartRun, and a one-shot child resumes by asking
		// claude to load its own prior session (Instance.Resume).
		DelegatedChildren: engine.Provide(engine.DelegatedChildren{ResumesByKey: true}),
		// What delivery writes into the working tree: the settings, commands
		// and skills dir, the MCP file, and the context file.
		ProjectArtifacts: []string{ConfigDirName + "/", MCPFileName, ContextFileName},
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

// Declaration is agent.Hosted's: the approach names a binding may select per
// surface kind (`agent edit --surface`), each kind's default first. A static
// table: delivery is the typed approaches' (Build), driven by the static
// writer, and nothing is constructed from a name.
func (Claude) Declaration() agent.Declaration {
	return agent.Declaration{
		agent.SurfaceContext:  agent.Presents(agent.ApproachUnsafeFile, ApproachSystemPrompt),
		agent.SurfaceMCP:      agent.Presents(ApproachMCPConfig, agent.ApproachUnsafeFile),
		agent.SurfaceSettings: agent.Presents(agent.ApproachUnsafeFile),
		agent.SurfaceCommands: agent.Presents(agent.ApproachUnsafeFile),
		agent.SurfaceSkills:   agent.Presents(agent.ApproachUnsafeFile),
	}
}

// Backend is agent.Hosted's: a fresh backend over the injected launcher.
func (c Claude) Backend(launch agent.Launcher) agent.Backend {
	b := newClaudeCode(c)
	b.SetLauncher(launch)
	return b
}

// NewConfig is agent.Hosted's: the zero typed config a labeled entry's body
// decodes into.
func (Claude) NewConfig() agent.BackendConfig { return &ClaudeConfig{} }

// SettingsReader is agent.Hosted's: the status read over claude's settings
// files.
func (Claude) SettingsReader(o agent.SettingsOptions) agent.SettingsReader { return NewWriter(o) }

// HookGlobalScope is agent.Hosted's. claude's project settings.json
// collapses onto its user-global one exactly when workDir == $HOME — found
// live (`manage hooks install` run from $HOME silently went global).
func (Claude) HookGlobalScope() (agent.HookGlobalScope, bool) {
	return agent.HookGlobalScope{
		Paths: func(workDir string) (string, string, error) {
			global, err := GlobalSettingsPath()
			return ProjectSettingsPath(workDir), global, err
		},
		Label: "Claude Code's user-global settings file",
	}, true
}

// EngineCLIs is the L1 grammar the standalone mock engine impersonates
// (agent.EngineCLIProvider), read off the engine value.
func (Claude) EngineCLIs() []agent.EngineCLI { return ClaudeEngineCLIs() }

var (
	_ agent.Hosted            = Claude{}
	_ agent.EngineCLIProvider = Claude{}
)

// approach is the name and traits every typed approach here carries
// (kit.Approach), private: whatever lands under the session home needs that
// home rooted, and a run without one is refused rather than served from the
// user's real home (agent.SessionHomeRooted).
func approach(name string, t present.Traits) kit.Approach {
	return kit.Approach{Engine: EngineName, ApproachName: name, T: t, Private: true}
}

// contextApproach is claude's context surface: the framed system prompt,
// announced on --append-system-prompt-file, under the session home; at the
// project root (a materialize, or a binding that selects the shared root)
// the well-known CLAUDE.md, the assembled context appended to whatever the
// file already holds. The ownership record owns the write and restores the
// prior bytes on removal — there is no marker section to parse.
type contextApproach struct{ kit.Approach }

func (a *contextApproach) DeliverContext(start present.Start, root present.RootKind, in engine.ContextInputs, fs afero.Fs) (present.Delivered, error) {
	switch root {
	case present.RootProjectRoot:
		// The user's CLAUDE.md is theirs; the record owns what is appended.
		return kit.AppendedSection(start.UnderProjectRoot(ContextFileName).Build(), in.Text), nil
	case present.RootSessionHome:
	default:
		return present.Delivered{}, a.ErrRoot(root)
	}
	s := &systemPromptContext{content: string(in.Text), fs: agent.GetFS(fs)}
	if _, err := s.Deliver(start); err != nil {
		return present.Delivered{}, err
	}
	out := present.Delivered{Presented: s.Present(start)}
	if s.Path() != "" {
		out.Files = []string{s.Path()}
	}
	return out, nil
}

// mcpApproach is claude's MCP surface: .mcp.json under the session home
// (announced on --mcp-config) or, when the plan selects the shared root, the
// project's own well-known .mcp.json.
type mcpApproach struct{ kit.Approach }

// DeliverMCP claims each server's entry in the .mcp.json under the root: the
// private session-home file announced on --mcp-config, or the project's own
// .mcp.json, whose relay bearer is named by reference (bearerByReference) so a
// file teams commit never holds it. The private file is claimed even with no
// servers: --mcp-config names it whatever the run registers, and claude
// refuses to start against a path that does not exist. The project file is
// the user's and is never conjured.
func (a *mcpApproach) DeliverMCP(start present.Start, root present.RootKind, in engine.MCPInputs, _ afero.Fs) (present.Delivered, error) {
	r, err := a.Rooted(start, root, MCPFileName, MCPFileName)
	if err != nil {
		return present.Delivered{}, err
	}
	switch root {
	case present.RootSessionHome:
		p := r.AnnounceFlag(flagMCPConfig).Build()
		claims, err := mcpClaims(in.Servers)
		if err != nil {
			return present.Delivered{}, err
		}
		if len(claims) == 0 {
			claims = []present.Claim{{Pointer: present.PointerKey(mcpServersKey), Value: map[string]any{}}}
		}
		return present.Delivered{Presented: p, Claims: map[string][]present.Claim{p.HostPath: claims}}, nil
	case present.RootProjectRoot:
		bundle, env, err := bearerByReference(in.Servers)
		if err != nil {
			return present.Delivered{}, err
		}
		p := r.Build()
		if len(env) > 0 {
			p.Env = env
		}
		claims, err := mcpClaims(bundle)
		if err != nil {
			return present.Delivered{}, err
		}
		return present.Delivered{Presented: p, Claims: map[string][]present.Claim{p.HostPath: claims}}, nil
	}
	return present.Delivered{}, a.ErrRoot(root)
}

// mcpClaims is each server as a claim on its own entry under mcpServers,
// spelled as the chat scratch file spells it (desiredMCPServers), through the
// bundle that shipped it (its provenance stamp, wire.MCPServer.SCM).
func mcpClaims(servers map[string]wire.MCPServer) ([]present.Claim, error) {
	desired, err := (&ClaudeCodeHookWriter{}).desiredMCPServers(servers)
	if err != nil {
		return nil, err
	}
	claims := make([]present.Claim, 0, len(desired))
	for _, name := range collections.SortedKeys(desired) {
		claims = append(claims, present.Claim{Pointer: present.PointerKey(mcpServersKey) + present.PointerKey(name), Value: desired[name], Via: servers[name].SCM})
	}
	return claims, nil
}

// deliverSettingsFile claims ctxloom's entries in claude's settings file for
// the root — the well-known .claude/settings.json under the project root, or
// settings.json under the session home. The settings and hooks kinds both
// claim into this one file, because claude keeps both there; the static
// writer folds their claims into one write, and each writer's leave with it.
func deliverSettingsFile(a kit.Approach, start present.Start, root present.RootKind, claims func(path string) ([]present.Claim, error)) (present.Delivered, error) {
	r, err := a.Rooted(start, root, SettingsFileName, relSettings)
	if err != nil {
		return present.Delivered{}, err
	}
	p := r.Build()
	cs, err := claims(p.HostPath)
	if err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Claims: map[string][]present.Claim{p.HostPath: cs}}, nil
}

// bashTimeoutEnv is the engine-neutral shell timeout as the env claude's
// Bash tool reads it from, in milliseconds: the foreground default, and the
// most the model may ask for. Empty for the zero value, which says nothing.
func bashTimeoutEnv(st engine.ShellTimeout) map[string]string {
	if st == (engine.ShellTimeout{}) {
		return nil
	}
	return map[string]string{
		"BASH_DEFAULT_TIMEOUT_MS": strconv.FormatInt(st.Default.Milliseconds(), 10),
		"BASH_MAX_TIMEOUT_MS":     strconv.FormatInt(st.Max.Milliseconds(), 10),
	}
}

// settingsClaims claims the statusline when asked for and free to claim,
// each denied tool as an element of permissions.deny — a deny the user
// already has is theirs, and the record finds it rather than taking it —
// and each of the shell timeout's env the user has not set to a value of
// their own.
func settingsClaims(fs afero.Fs, path string, in engine.SettingsInputs) ([]present.Claim, error) {
	statusline, deny := in.Statusline, in.DenyTools
	claims, err := bashTimeoutClaims(fs, path, bashTimeoutEnv(in.ShellTimeout))
	if err != nil {
		return nil, err
	}
	if statusline {
		free, err := statuslineClaimable(fs, path)
		if err != nil {
			return nil, err
		}
		if free {
			claims = append(claims, present.Claim{Pointer: present.PointerKey("statusLine"),
				Value: map[string]any{"type": "command", "command": ctxloomStatusLineCommand()}})
		}
	}
	seen := map[string]bool{}
	for _, tool := range deny {
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		claims = append(claims, present.Claim{Pointer: present.PointerKey("permissions") + present.PointerKey("deny") + "/-", Value: tool})
	}
	return claims, nil
}

// bashTimeoutClaims claims each of want in settings.json's env that is
// absent or already holds ctxloom's value; one the user set to any other
// value is theirs.
func bashTimeoutClaims(fs afero.Fs, path string, want map[string]string) ([]present.Claim, error) {
	if len(want) == 0 {
		return nil, nil
	}
	w := &ClaudeCodeHookWriter{FS: fs}
	settings, err := w.loadSettings(path) // a missing file is empty settings
	if err != nil {
		return nil, err
	}
	env := map[string]json.RawMessage{}
	if raw, ok := settings.Other["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			data, readErr := afero.ReadFile(fs, path)
			if readErr != nil {
				return nil, readErr
			}
			return nil, w.corruptSettings(path, data, "env", err, "to avoid overwriting the user's environment")
		}
	}
	var claims []present.Claim
	for _, name := range collections.SortedKeys(want) {
		if have, set := env[name]; set {
			var v string
			if json.Unmarshal(have, &v) != nil || v != want[name] {
				continue
			}
		}
		claims = append(claims, present.Claim{Pointer: present.PointerKey("env") + present.PointerKey(name), Value: want[name]})
	}
	return claims, nil
}

// statuslineClaimable reports whether the statusline is ctxloom's to claim:
// there is none, or it is exactly ctxloom's canonical command. Any other —
// the user's own program, or the ctxloom binary with arguments the user
// chose — is the user's.
func statuslineClaimable(fs afero.Fs, path string) (bool, error) {
	settings, err := (&ClaudeCodeHookWriter{FS: fs}).loadSettings(path) // a missing file is empty settings
	if err != nil {
		return false, err
	}
	return settings.StatusLine == nil || settings.StatusLine.Command == ctxloomStatusLineCommand(), nil
}

// hookClaims is each unified hook, and each hook declared by claude's own
// event name (native), as a claim on its entry in the group of its event that
// its matcher selects — the user's own group included, so the file keeps the
// shape claude writes. A hook routed twice is claimed once.
func hookClaims(unified wire.UnifiedHooks, native wire.BackendHooks) ([]present.Claim, error) {
	var (
		claims []present.Claim
		seen   = map[string]bool{}
		failed error
	)
	claim := func(event string, h wire.Hook) {
		v, err := hookValue(h)
		if err != nil {
			failed = errors.Join(failed, err)
			return
		}
		ptr := present.PointerKey("hooks") + present.PointerKey(event) + present.PointerSelect("matcher", h.Matcher) + "/hooks/-"
		if key := ptr + fmt.Sprint(v); !seen[key] {
			seen[key] = true
			claims = append(claims, present.Claim{Pointer: ptr, Value: v})
		}
	}
	agent.RouteUnifiedHooks(report.To(strictness.Sink("ctxloom")), EngineName, unifiedHookRoutes(unified), claim)
	for _, event := range collections.SortedKeys(native) {
		for _, h := range native[event] {
			claim(event, h)
		}
	}
	return claims, failed
}

// hookValue is h as claude's settings.json spells a hook: claudeCodeHook's
// JSON, the type defaulting to "command".
func hookValue(h wire.Hook) (map[string]any, error) {
	cc := claudeCodeHook{Type: h.Type, Command: h.Command, Args: h.Args, Prompt: h.Prompt, Timeout: h.Timeout, Async: h.Async}
	if cc.Type == "" {
		cc.Type = "command"
	}
	b, err := json.Marshal(cc)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	return v, json.Unmarshal(b, &v)
}

// settingsApproach is claude's settings surface: ctxloom's own keys in
// .claude/settings.json (settingsClaims).
type settingsApproach struct{ kit.Approach }

func (a *settingsApproach) DeliverSettings(start present.Start, root present.RootKind, in engine.SettingsInputs, fs afero.Fs) (present.Delivered, error) {
	return deliverSettingsFile(a.Approach, start, root, func(path string) ([]present.Claim, error) {
		return settingsClaims(agent.GetFS(fs), path, in)
	})
}

// hooksApproach is claude's hooks surface: the hook registrations, claimed
// in the hooks section of the same settings.json the settings approach
// claims into — the native form (deliverSettingsFile). The unified set is
// first bound to claude (agent.BindHooks): ctxloom's callbacks name claude as
// the engine that fires them, and a hook narrowed to a tool class is narrowed
// to claude's tool for it (toolMatcher).
type hooksApproach struct{ kit.Approach }

func (a *hooksApproach) DeliverHooks(start present.Start, root present.RootKind, in engine.HooksInputs, _ afero.Fs) (present.Delivered, error) {
	bound, err := agent.BindHooks(in.Hooks, EngineName, toolMatcher)
	if err != nil {
		return present.Delivered{}, err
	}
	return deliverSettingsFile(a.Approach, start, root, func(string) ([]present.Claim, error) { return hookClaims(bound, in.Ext[EngineName]) })
}

// commandsApproach is claude's commands surface: <config dir>/commands/
// under the session home (the user-level directory claude loads alongside a
// project's), or .claude/commands/ under the project root when the binding
// selects it; a command's help text and metadata arrive already decoded
// from its block (Claude.Exports) and become the slash-command frontmatter.
type commandsApproach struct{ kit.Approach }

// DeliverCommands writes the commands as claude slash-command files
// (renderCommand). The session's config dir is its own instance:
// nothing in the user's real ~/.claude/commands is deduped against. At the
// project root the plan's commands land as given too: a copy in the
// materializing host's own ~/.claude/commands is no reason to withhold one
// from a tree that will be read elsewhere.
func (a *commandsApproach) DeliverCommands(start present.Start, root present.RootKind, in engine.CommandsInputs, files safefs.Root) (present.Delivered, error) {
	return kit.DeliverCommands(a.Approach, start, root, CommandsDirName, relCommands, files, in, renderCommand, nil)
}

// skillsApproach is claude's skills surface: <config dir>/skills/<name>/
// under the session home (the user-level directory claude loads alongside a
// project's), or .claude/skills/<name>/ under the project root when the
// binding selects it. Only the skills claude will load are written
// (acceptedSkills).
type skillsApproach struct{ kit.Approach }

func (a *skillsApproach) DeliverSkills(start present.Start, root present.RootKind, in engine.SkillsInputs, files safefs.Root) (present.Delivered, error) {
	return kit.DeliverSkills(a.Approach, start, root, SkillsDirName, relSkills, files, in, acceptedSkills, nil)
}

// RelayCommand is the hidden ctxloom subcommand claude spawns as its ctxloom
// MCP server: the session relay (internal/engines/claude/relay). It relays
// MCP between claude's stdio and the session's endpoint, and posts claude's
// wake as claude's own descendant — the one standing that makes the post
// self-sent. EnvRelayURL and EnvRelayBearer hand it the endpoint.
const (
	RelayCommand   = "claude-relay"
	EnvRelayURL    = "CTXLOOM_CLAUDE_RELAY_URL"
	EnvRelayBearer = "CTXLOOM_CLAUDE_RELAY_BEARER"
)

// sessionEndpoint is the dynamic approach claude PROVIDES: the session's MCP
// endpoint reached through claude's own relay, a stdio entry whose env names
// the endpoint and its bearer. Only claude renders this entry; no other engine
// spawns the relay.
type sessionEndpoint struct{ kit.Approach }

func (*sessionEndpoint) Endpoint(ep sessions.Endpoint) wire.MCPServer {
	return wire.MCPServer{
		Command: agent.CtxloomCommand(),
		Args:    []string{RelayCommand},
		Env:     map[string]string{EnvRelayURL: ep.URL, EnvRelayBearer: ep.Credential},
	}
}

// Compile-time contracts: each typed approach fills its kind's field.
var (
	_ engine.ContextApproach  = (*contextApproach)(nil)
	_ engine.MCPApproach      = (*mcpApproach)(nil)
	_ engine.SettingsApproach = (*settingsApproach)(nil)
	_ engine.HooksApproach    = (*hooksApproach)(nil)
	_ engine.CommandsApproach = (*commandsApproach)(nil)
	_ engine.SkillsApproach   = (*skillsApproach)(nil)
	_ engine.DynamicApproach  = (*sessionEndpoint)(nil)
)
