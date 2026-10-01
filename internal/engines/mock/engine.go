package mock

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// This file is the mock ENGINE KIND: the conformance double and the first
// Engine implementer. Its typed approaches are REAL files (surfaces.go):
// each writes its kind's native form under the root the plan selected, so a
// test asserts what was delivered by reading it, without an engine binary —
// and its turn reads the delivered hook file back to FIRE hooks (hooks.go).
// It provides no dynamic approach unless a test asks (WithDynamic): the
// static half only, everything through its typed approaches, exactly as
// Base.Delegate decides. The bare kind (New) has no image, so Container
// refuses; the SHIPPED doubles (Doubles) carry one (WithContainer), because
// the isolation matrix runs on them.

// Name is the mock kind's registry name. The three doubles are the same
// kind under another name with one declared difference each: Lossy carries
// every surface and drops two hook kinds at export time; Launch keeps only
// its context surface (the rest arrive per session, inside an engine home);
// NoSkills carries a skills surface and exports nothing to it.
const (
	Name         engine.Name = "mock"
	NameLossy    engine.Name = "mock-lossy"
	NameLaunch   engine.Name = "mock-launch"
	NameNoSkills engine.Name = "mock-noskills"
)

// Mock is the engine KIND: the engine root embedded (its Definition, the
// views and the common decisioning), the engine-specific logic as methods.
// Built once by New; immutable. noSkillExport is the NoSkills double's one
// declared difference: it exports no skill package, whatever it is handed.
type Mock struct {
	engine.Base
	noSkillExport bool
	// fires is the unified hook events this kind fires (hooks.go); the
	// lossy double drops two.
	fires       map[string]bool
	home        engine.HomeSpec
	container   *engine.ContainerSpec
	transcripts []engine.TranscriptReader
	// hookScope is a declared project/global settings-path collision
	// (WithHookGlobalScope), for a test standing a guarded engine in.
	hookScope *agent.HookGlobalScope
}

// Option adjusts the kind before Validate.
type Option func(*Mock)

// WithContainer gives the kind an image: mock installs NO vendor CLI — its
// engine is the ctxloom binary itself, which the image composer copies in
// after every engine fragment regardless. The fragment's only job is to be
// non-nil (so the spec is composable) and to assert the one mock-specific
// need — `cat`, for the shared-filesystem probe — as a build-time gate
// rather than an assumption. NOT a template for a real engine, whose
// fragment must install and validate a real client. mock declares no Auth:
// it authenticates against no vendor, so a container run of it needs no
// credentials.
func WithContainer() Option {
	return func(m *Mock) {
		m.container = &engine.ContainerSpec{
			Install:         installFragment,
			ValidateCommand: "cat --version",
			OverlayDirs:     []string{ConfigDirName},
			// mock keeps no transcripts, so there is no native store root to
			// bind-mount.
			TranscriptStoreRel: "",
		}
	}
}

// WithContainerSpec gives the kind the container story a test declares.
func WithContainerSpec(c engine.ContainerSpec) Option {
	return func(m *Mock) { m.container = &c }
}

// WithHome gives the kind a relocatable home: the fixture a test of the
// home-seeding path declares. The shipped mock keeps none.
func WithHome(h engine.HomeSpec) Option {
	return func(m *Mock) { m.home = h }
}

// WithTranscripts hands the kind the readers of its (degenerate) transcript
// store: a transcript adapter's values, composed at the root.
func WithTranscripts(readers ...engine.TranscriptReader) Option {
	return func(m *Mock) { m.transcripts = append(m.transcripts, readers...) }
}

// ConfigDirName is the mock engine's project-relative managed-config
// directory: the one directory its container overlays shadow.
const ConfigDirName = ".mock"

// installFragment asserts `cat` (the shared-fs probe runs `cat /probe/marker`
// in the image). See WithContainer.
var installFragment = []byte(`RUN command -v cat >/dev/null 2>&1 \
    || { echo "ctxloom: this base has no cat (needed by the shared-fs probe, sharedfs.go's probeOneRoot)" >&2; exit 1; }
`)

// Without nils the typed field for each kind: the lossy variant the
// uncarried and requiredness tests use.
func Without(kinds ...present.Kind) Option {
	return func(m *Mock) {
		d := &m.Definition
		for _, k := range kinds {
			switch k {
			case present.Context:
				d.Context = nil
			case present.MCP:
				d.MCP = nil
			case present.Settings:
				d.Settings = nil
			case present.Hooks:
				d.Hooks = nil
			case present.Commands:
				d.Commands = nil
			case present.Skills:
				d.Skills = nil
			}
		}
	}
}

// WithoutHookEvents drops unified hook events from the kind: it fires
// none of them and exports none of them (the lossy double's declared
// difference — TWO events, so a report that groups several is exercised).
func WithoutHookEvents(events ...string) Option {
	return func(m *Mock) {
		if m.HookLosses == nil {
			m.HookLosses = map[string]string{}
		}
		for _, e := range events {
			delete(m.fires, e)
			// Each event names its own reason so a report cannot attribute
			// one event's absence to another's cause.
			m.HookLosses[e] = string(m.Name) + " has no native " + e + " event"
		}
	}
}

// WithHookGlobalScope declares a project/global settings-path collision
// class shaped like a guarded engine's own, so a test can prove the guard
// without naming a real engine.
func WithHookGlobalScope(s agent.HookGlobalScope) Option {
	return func(m *Mock) { m.hookScope = &s }
}

// WithDistribution sets the shipping policy (registry fixtures use it to
// stand in for a shippable engine).
func WithDistribution(d engine.Distribution) Option {
	return func(m *Mock) { m.Distribution = d }
}

// WithDynamic provides a dynamic approach (the delegation tests use it).
func WithDynamic() Option {
	return func(m *Mock) { m.Dynamic = &endpointEntry{name: "session-endpoint"} }
}

// endpointEntry is the mock's dynamic approach: it names the session
// endpoint as a plain URL entry with the bearer as a header.
type endpointEntry struct{ name string }

func (e *endpointEntry) Name() string { return e.name }
func (e *endpointEntry) Traits() present.Traits {
	return present.Traits{Roots: []present.RootKind{present.RootSessionHome}, Channel: present.ChannelFile}
}
func (e *endpointEntry) Endpoint(ep sessions.Endpoint) wire.MCPServer { return engine.BearerEntry(ep) }

// WithoutGrammar drops a mode's argv grammar (the incoherence test uses it).
func WithoutGrammar(mode engine.Mode) Option {
	return func(m *Mock) {
		var keep []engine.CLIGrammar
		for _, g := range m.CLI {
			if g.Mode != mode {
				keep = append(keep, g)
			}
		}
		m.CLI = keep
	}
}

// New and NewNamed build the constant, coherent mock kind; a failure is a
// programming error in this file, so they panic rather than return it.
func New(opts ...Option) engine.Engine { return NewNamed(Name, opts...) }

// NewNamed builds the same kind under another name (the polymorphism proof:
// two names, identical plans).
func NewNamed(name engine.Name, opts ...Option) engine.Engine {
	e, err := Build(name, opts...)
	if err != nil {
		panic(err)
	}
	return e
}

// Doubles builds the mock kind and its three doubles, the set the
// composition root registers — each with the container the isolation matrix
// runs on, and the readers the root hands every one of them.
func Doubles(opts ...Option) []engine.Engine {
	shipped := append([]Option{WithContainer()}, opts...)
	return []engine.Engine{
		New(shipped...),
		NewNamed(NameLossy, append(shipped, WithoutHookEvents("session_start", "session_end"))...),
		NewNamed(NameLaunch, append(shipped, Without(present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills))...),
		NewNoSkills(shipped...),
	}
}

// NewNoSkills is the double that exports no skill: THE ABSENCE IS THE
// ENTIRE POINT. Every other kind exports skills, which would leave the
// missing-skills arm of every caller with nothing to point at; it is a
// declared difference, so it cannot be "completed" by accident.
func NewNoSkills(opts ...Option) engine.Engine {
	m := NewNamed(NameNoSkills, opts...).(Mock)
	m.noSkillExport = true
	return m
}

// Build is THE CONSTRUCTOR: the one place this engine's declaration is
// assembled and the one place an incoherent one is refused (a mode with no
// grammar, a nameless or rootless approach). Kinds need no check: the typed
// fields of Definition make a missing or duplicate kind a compile error.
// The shape is the plain constructor — options applied, Validate once. A
// typestate builder is rejected as non-obvious machinery and named only as
// the fallback if requiredness must ever become compile-time.
func Build(name engine.Name, opts ...Option) (engine.Engine, error) {
	// Every mock surface is a file it can read from the session home or,
	// when the binding selects the shared root, the project root — the
	// session home first, so the default keeps the project tree clean.
	shared := []present.RootKind{present.RootSessionHome, present.RootProjectRoot}
	// Every surface is a FILE the mock opens (the context file is a
	// well-known file it reads at rest, ctxloom in the loop or not); the
	// context and hook files are also announced on argv so a session home
	// the mock was not started in can be found.
	file := present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true}
	flags := []engine.Flag{{Name: "--resume", HasValue: true}, {Name: contextFlag, HasValue: true}, {Name: HooksFlag, HasValue: true}}
	d := engine.Definition{
		Name:         name,
		Distribution: engine.DistributionTestOnly,
		Modes:        []engine.Mode{engine.Interactive, engine.Structured},
		// ReadOnlyPlan: the mock never runs tools, so it is read-only by
		// construction.
		Context:  &contextFile{surface{"context-file", file}},
		MCP:      &mcpFile{surface{"mcp-config", file}},
		Settings: &settingsFile{surface{"settings", file}},
		Hooks:    &hooksFile{surface{"hooks-file", file}},
		Commands: &commandsDir{surface{"commands-dir", file}},
		Skills:   &skillsDir{surface{"skills-dir", file}},
		CLI: []engine.CLIGrammar{
			{Mode: engine.Interactive, Binary: "mock", Flags: flags},
			{Mode: engine.Structured, Binary: "mock", Flags: flags},
		},
		ModelAliases: map[string]string{},
		ExportSchema: []byte(`{"type":"object"}`),
	}
	m := Mock{Base: engine.Base{Definition: d}, fires: map[string]bool{}}
	for _, e := range hookEvents {
		m.fires[e] = true
	}
	for _, o := range opts {
		o(&m)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.container != nil {
		if err := m.container.Validate(); err != nil {
			return nil, err
		}
	}
	if err := m.home.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Home: mock keeps NO engine-global config or credential state — a bare
// echo compiled into ctxloom that never spawns a grandchild and never
// touches disk — so the null object, unless a test declared one
// (WithHome): nothing relocates, nothing seeds.
func (m Mock) Home() engine.HomeSpec { return m.home }

// Container is the image WithContainer gave the kind, or the refusal: the
// bare conformance double has none.
func (m Mock) Container() (engine.ContainerSpec, error) {
	if m.container == nil {
		return engine.ContainerSpec{}, engine.ErrUnsupported{Engine: m.Name, Capability: "container"}
	}
	return *m.container, nil
}

// Transcripts are the readers WithTranscripts handed the kind; none on the
// bare double.
func (m Mock) Transcripts() []engine.TranscriptReader { return m.transcripts }

// Hooks decodes the payload the mock's own turn writes to a hook's stdin
// (hooks.go).
func (m Mock) Hooks() engine.HookCodec { return hookCodec{m.Name} }

// Wake is declared absent: the mock has no out-of-band way to start a turn.
func (m Mock) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Absent[engine.WakeSpec]("the mock listens on nothing a wake could post to")
}

// Instance is where REQUIREDNESS is checked, loudly: the mock cannot run a
// session without a context surface to carry the system prompt.
func (m Mock) Instance(s engine.Session) (engine.Instance, error) {
	if m.Context == nil {
		return nil, engine.ErrUnsupported{Engine: m.Name, Capability: "context"}
	}
	return &instance{s: s, fires: m.fires}, nil
}

type instance struct {
	s     engine.Session
	key   string
	fires map[string]bool
}

// Exec composes the process from the presentations in delivery order: each
// one's argv channel (the context and hook files) and env, after the home
// vars and the resume key.
func (i *instance) Exec(presented []present.Presentation) (engine.Exec, error) {
	env := map[string]string{}
	for _, h := range i.s.Home {
		env[h.Var] = h.Path
	}
	args := []string{}
	if i.key != "" {
		args = append(args, "--resume", i.key)
	}
	for _, p := range presented {
		args = append(args, p.Args...)
		for k, v := range p.Env {
			env[k] = v
		}
	}
	return engine.Exec{Binary: "mock", Args: args, Env: env, WorkDir: i.s.WorkDir, Interactive: i.s.Mode == engine.Interactive}, nil
}
func (i *instance) Drivers() []engine.StructuredDriver {
	deny, _ := mockRules(i.s.Permission.Posture.Document["deny"]) // none declared: nothing denied
	mode, _ := i.s.Permission.Posture.Document["mode"].(string)
	return []engine.StructuredDriver{driver{fires: i.fires, approver: i.s.Permission.Approver, mode: mode, deny: deny, endpoint: i.s.MCP}}
}
func (i *instance) Resume(key string) error { i.key = key; return nil }

// driver is the mock's structured driver (turn.go): it fires the delivered
// hooks for every event the turn passes through and echoes the prompt. A
// mock:ask turn asks its session's approver through the session's endpoint
// (ask.go).
type driver struct {
	fires    map[string]bool
	approver engine.Approver
	// mode is the session's declared posture: a turn that asks for none
	// runs at it.
	mode string
	// deny are the session's declared deny rules (a mock rule is a tool
	// name): they refuse a call before any grant or approver is consulted.
	deny     []string
	endpoint sessions.Endpoint
}

// Exports exports EVERYTHING: no bundle carries a block for a mock (mock is
// a test engine nobody publishes a bundle FOR), so there is no opt-out to
// read and nothing to invent one from — a mock that silently exported
// nothing would be a commands surface that reports success and writes zero
// bytes, precisely the silent no-op the mock exists to catch in others. The
// NoSkills double exports no skill: that absence is the subject of every
// missing-skills-surface arm.
func (m Mock) Exports(items engine.Items) (engine.Exports, error) {
	// The events this kind fires (hooks.go); the mock translates nothing,
	// so the unified name is the native one.
	out := engine.Exports{HookEvent: map[string]string{}}
	for e := range m.fires {
		out.HookEvent[e] = e
	}
	for _, item := range items.Commands {
		out.Commands = append(out.Commands, engine.CommandExport{Name: item.Name, Body: item.Body, Enabled: true})
	}
	if !m.noSkillExport {
		for _, item := range items.Skills {
			out.Skills = append(out.Skills, engine.SkillExport{Name: item.Name, Description: item.Description, Files: item.Files, Enabled: true})
		}
	}
	return out, nil
}

var _ engine.Engine = Mock{}

// SettingsWriter is agent.Hosted's: the writer over .mock/settings.json.
func (Mock) SettingsWriter(opts agent.SettingsOptions) agent.SettingsWriter {
	return NewMockSettingsWriter(opts)
}

// HookGlobalScope is agent.Hosted's: none unless declared (WithHookGlobalScope)
// — the mock's settings surface is a project-relative file with no
// user-global twin to collapse onto.
func (m Mock) HookGlobalScope() (agent.HookGlobalScope, bool) {
	if m.hookScope == nil {
		return agent.HookGlobalScope{}, false
	}
	return *m.hookScope, true
}

var _ agent.Hosted = Mock{}
