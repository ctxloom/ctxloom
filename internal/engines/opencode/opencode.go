// Package opencode is the opencode engine KIND: the opencode CLI driven
// against an OpenAI-compatible endpoint the label names (a LAN model server),
// one `opencode run --format json` process per structured turn, resumed by
// its native session key. It is ASSEMBLED from internal/engines/kit — the
// per-turn process driver, the Exec composition, the approach rooting, the
// framed context file, the MCP table, the managed command and skill trees,
// the export decoder and the projected backend — plus opencode's own content:
// its argv, its JSONL line codec and failure table, its config dialect
// (opencode.json claims, OPENCODE_CONFIG_CONTENT), its permission postures
// and its one-variable auth.
//
// Everything is delivered into the session home H, the directory
// OPENCODE_CONFIG_DIR names (opencode's highest-precedence config dir): the
// framed AGENTS.md, opencode.json (MCP servers, denied tools), commands/ and
// skills/. Every XDG base is relocated beneath H as a merged tree whose
// opencode app dirs are the session's own, so nothing of the user's real
// opencode state is read, and opencode's database (its native history) lives
// in the data base's app dir, linked into the session's native/ so a resumed
// child finds its conversation after the home is gone.
package opencode

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// EngineName is the registry key and the config `type` that selects opencode.
const EngineName engine.Name = "opencode"

// Opencode is the engine KIND: the engine root embedded, opencode's logic as
// methods. Built once by Build; immutable. open and now are the structured
// driver's seams (a turn run against in-memory pipes, a fixed clock).
type Opencode struct {
	engine.Base
	open kit.TransportFunc
	now  func() time.Time
}

// Option adjusts the kind before Validate.
type Option func(*Opencode)

// WithVersion hands the kind the reading of `opencode --version`: the parse
// is the version adapter's, so the composition root supplies it.
func WithVersion(v engine.VersionCommand) Option {
	return func(o *Opencode) { o.Version = v }
}

// WithTransport replaces the spawned `opencode run` process (a test's seam).
func WithTransport(open kit.TransportFunc) Option {
	return func(o *Opencode) { o.open = open }
}

// The approach names a binding selects, one per kind: opencode delivers
// every kind into the session home and nowhere else in this release.
const (
	ApproachAgentsFile   = "agents-file"
	ApproachConfigFile   = "config-file"
	ApproachConfigPolicy = "config-permissions"
	ApproachCommandsDir  = "commands-dir"
	ApproachSkillsDir    = "skills-dir"
	ApproachEndpoint     = "session-endpoint"
)

// Build is THE CONSTRUCTOR: opencode's declaration assembled once and
// refused by engine.Base.Validate (and its home by HomeSpec.Validate) when
// incoherent.
func Build(opts ...Option) (engine.Engine, error) {
	home := []present.RootKind{present.RootSessionHome}
	file := present.Traits{Roots: home, Channel: present.ChannelFile, Persists: true}
	d := engine.Definition{
		Name: EngineName,
		// Opt-in: a label must name `type: opencode`; never the default.
		Distribution: engine.DistributionOptIn,
		// The structured runner; the TUI is a later slice.
		Modes:    []engine.Mode{engine.Structured},
		Context:  &contextApproach{approach(ApproachAgentsFile, file)},
		MCP:      &mcpApproach{approach(ApproachConfigFile, file)},
		Settings: &settingsApproach{approach(ApproachConfigPolicy, present.Traits{Roots: home, Channel: present.ChannelFile})},
		Commands: &commandsApproach{approach(ApproachCommandsDir, file)},
		Skills:   &skillsApproach{approach(ApproachSkillsDir, file)},
		// No hooks approach: opencode has no external hook-command protocol,
		// only in-process plugins (a generated bridge plugin is a later
		// slice). A structured child gets its mail as turns.
		Dynamic:      &sessionEndpoint{approach(ApproachEndpoint, present.Traits{Roots: home, Channel: present.ChannelFile})},
		CLI:          []engine.CLIGrammar{structuredGrammar},
		ModelAliases: map[string]string{},
		ExportSchema: ExportSchema,
		// A one-shot child resumes by naming its own session (--session).
		DelegatedChildren: engine.Provide(engine.DelegatedChildren{ResumesByKey: true}),
	}
	o := Opencode{Base: engine.Base{Definition: d}}
	for _, opt := range opts {
		opt(&o)
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if err := o.Home().Validate(); err != nil {
		return nil, err
	}
	return o, nil
}

var (
	_ engine.Engine = Opencode{}
	_ agent.Hosted  = Opencode{}
)

// approach is the name and traits every typed approach carries, private:
// whatever lands under the session home needs that home rooted.
func approach(name string, t present.Traits) kit.Approach {
	return kit.Approach{Engine: EngineName, ApproachName: name, T: t, Private: true}
}

// The vars that relocate opencode: OPENCODE_CONFIG_DIR names the session home
// H itself (where every delivery lands), each XDG base is a merged tree
// beneath it owning opencode's app dir, and opencode's two test levers point
// its notion of the user's home (~/.opencode, ~/.claude/CLAUDE.md, the skills
// dirs) and its managed-config dir (/etc/opencode) at directories of the
// session's own, so no host file outranks ctxloom's config.
const (
	envConfigDir     = "OPENCODE_CONFIG_DIR"
	envTestHome      = "OPENCODE_TEST_HOME"
	envManagedConfig = "OPENCODE_TEST_MANAGED_CONFIG_DIR"
	// HomeLeaf is the session home's own leaf.
	HomeLeaf = "opencode"
	// appDir is opencode's app dir under every XDG base.
	appDir = "opencode"
	// dataBase is the data base's directory beneath H; opencode keeps its
	// database (opencode.db), the native history, in its app dir there.
	dataBase = ".xdg/data"
)

// Home declares the relocation (Q-B, Q-L FINAL): see the vars above. Auth is
// one required variable (opencodeAuth). No instance config is generated:
// ctxloom's provider and policy reach opencode in OPENCODE_CONFIG_CONTENT.
func (o Opencode) Home() engine.HomeSpec {
	owns := func() *engine.XDGMerge { return &engine.XDGMerge{Owns: []string{appDir}} }
	return engine.HomeSpec{
		Vars: []engine.HomeVar{
			{Name: envConfigDir, Subdir: HomeLeaf},
			{Name: "XDG_DATA_HOME", Subdir: dataBase, Merge: owns()},
			{Name: "XDG_CONFIG_HOME", Subdir: ".xdg/config", Merge: owns()},
			{Name: "XDG_STATE_HOME", Subdir: ".xdg/state", Merge: owns()},
			{Name: "XDG_CACHE_HOME", Subdir: ".xdg/cache", Merge: owns()},
			{Name: envTestHome, Subdir: "home"},
			{Name: envManagedConfig, Subdir: "managed"},
		},
		Auth:               engine.Provide[engine.Auth](opencodeAuth{}),
		TranscriptStoreRel: dataBase + "/" + appDir,
	}
}

// Container refuses: the pinned release-tarball image is a later slice.
func (o Opencode) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{}, engine.ErrUnsupported{Engine: o.Name, Capability: "container"}
}

// Transcripts: none. A structured run's transcript is the runner's own tee
// of the driver's events; opencode's database is not read.
func (Opencode) Transcripts() []engine.TranscriptReader { return nil }

// TranscriptSession refuses: no transcript of opencode's own is read.
func (o Opencode) TranscriptSession(string) (string, error) {
	return "", engine.ErrUnsupported{Engine: o.Name, Capability: "transcript session"}
}

// Hooks is a codec that refuses: opencode fires no ctxloom hook command, so
// no payload ever arrives.
func (o Opencode) Hooks() engine.HookCodec { return noHooks{o.Name} }

// Wake: no interactive owner to wake in this release.
func (Opencode) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Absent[engine.WakeSpec]("opencode runs structured only; an idle structured run is woken by its next turn")
}

// Approvals: none can be routed.
func (Opencode) Approvals() engine.Declared[engine.ApprovalCodec] {
	return engine.Absent[engine.ApprovalCodec]("`opencode run` rejects every permission ask itself, and its permission.ask plugin hook is never invoked (upstream #47654); an opencode label runs with approver: none")
}

// Trust: opencode keeps no record of a human trusting a repository.
func (Opencode) Trust() engine.Declared[engine.RepoTrust] {
	return engine.Absent[engine.RepoTrust]("opencode keeps no record of a human trusting a repository; every repository is untrusted: its project config is disabled and its AGENTS.md re-added by path")
}

// Declaration is agent.Hosted's: the one approach name per kind.
func (Opencode) Declaration() agent.Declaration {
	return agent.Declaration{
		agent.SurfaceContext:  agent.Presents(ApproachAgentsFile),
		agent.SurfaceMCP:      agent.Presents(ApproachConfigFile),
		agent.SurfaceSettings: agent.Presents(ApproachConfigPolicy),
		agent.SurfaceCommands: agent.Presents(ApproachCommandsDir),
		agent.SurfaceSkills:   agent.Presents(ApproachSkillsDir),
	}
}

// Backend is agent.Hosted's: the projection onto this kind.
func (o Opencode) Backend(launch agent.Launcher) agent.Backend {
	return kit.NewProjectedBackend(o, launch, backendDefaults)
}

// backendDefaults is what the projected backend needs beyond the kind.
var backendDefaults = kit.BackendDefaults{
	Engine:   string(EngineName),
	Binary:   defaultBinary,
	Provider: "openai-compatible",
	HomeVar:  envConfigDir,
	Policy:   defaultPolicy,
	Local: func(cfg agent.BackendConfig) (string, []string, error) {
		c, ok := cfg.(*Config)
		if !ok {
			return "", nil, kit.ErrConfigType(string(EngineName), cfg)
		}
		return c.BinaryPath, c.Args, nil
	},
}

// NewConfig is agent.Hosted's: the zero typed config a label decodes into.
func (Opencode) NewConfig() agent.BackendConfig { return &Config{} }

// SettingsReader is agent.Hosted's: nothing of ctxloom's is wired into the
// user's own files (every delivery lands in the session home), so the status
// is always unwired.
func (Opencode) SettingsReader(agent.SettingsOptions) agent.SettingsReader { return unwired{} }

// HookGlobalScope is agent.Hosted's: no settings file of the user's is
// written, so there is no project/global collision.
func (Opencode) HookGlobalScope() (agent.HookGlobalScope, bool) {
	return agent.HookGlobalScope{}, false
}

type unwired struct{}

func (unwired) Status(string) (agent.SettingsStatus, error) { return agent.SettingsStatus{}, nil }

// noHooks is the refusing codec.
type noHooks struct{ name engine.Name }

func (c noHooks) Decode(string, []byte) (engine.HookEvent, error) {
	return engine.HookEvent{}, engine.ErrUnsupported{Engine: c.name, Capability: "hooks"}
}

func (c noHooks) Encode(string, engine.HookResponse) (engine.HookReply, error) {
	return engine.HookReply{}, engine.ErrUnsupported{Engine: c.name, Capability: "hooks"}
}

func (noHooks) ContextLimit() int                          { return 0 }
func (noHooks) InvokedSkill(string, []byte) (string, bool) { return "", false }
