package mock

import (
	"context"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// This file is the mock ENGINE KIND: the conformance double and the first
// Engine implementer. Its typed approaches are OBSERVABLE no-ops — each
// writes a marker file under the selected root, so a test can assert what
// was delivered without an engine binary. It provides no dynamic approach
// unless a test asks (WithDynamic): the static half only, everything through
// its typed approaches, exactly as Base.Delegate decides. The bare kind
// (New) has no image, so Container refuses; the SHIPPED doubles (Doubles)
// carry one (WithContainer), because the isolation matrix runs on them.

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
	container     *engine.ContainerSpec
	transcripts   []engine.TranscriptReader
}

// Option adjusts the kind before Validate.
type Option func(*Mock)

// WithContainer gives the kind an image: mock installs NO vendor CLI — its
// engine is the ctxloom binary itself, which the image composer copies in
// after every engine fragment regardless. The fragment's only job is to be
// non-nil (so the spec is composable) and to assert the one mock-specific
// need — `cat`, for the shared-filesystem probe — as a build-time gate
// rather than an assumption. NOT a template for a real engine, whose
// fragment must install and validate a real client. mock authenticates
// against no vendor: there is no API key, token or credential file it could
// need, so resolution always succeeds with nothing — a POSITIVE fact about
// this one engine, verified by reading its implementation.
func WithContainer() Option {
	return func(m *Mock) {
		m.container = &engine.ContainerSpec{
			Install:         installFragment,
			ValidateCommand: "cat --version",
			Auth:            engine.Provide(engine.ContainerAuth{Vendorless: string(m.Name) + " authenticates against no vendor"}),
			OverlayDirs:     []string{ConfigDirName},
			// mock keeps no transcripts, so there is no native store root to
			// bind-mount.
			TranscriptStoreRel: "",
		}
	}
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
		NewNamed(NameLossy, shipped...),
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
	homeFile := []present.RootKind{present.RootSessionHome}
	shared := []present.RootKind{present.RootSessionHome, present.RootProjectRoot}
	d := engine.Definition{
		Name:         name,
		Distribution: engine.DistributionTestOnly,
		Modes:        []engine.Mode{engine.Interactive, engine.Structured},
		Permissions:  engine.PermissionFacts{Native: []engine.PermissionMode{engine.PermissionDefault, engine.PermissionBypass}, HostDefault: engine.PermissionDefault},
		Context:      &marker{"system-prompt", present.Traits{Roots: homeFile, Channel: present.ChannelArgv, LaunchOnly: true}, "context.md"},
		MCP:          &marker{"mcp-config", present.Traits{Roots: shared, Channel: present.ChannelFile, Persists: true}, ".mcp.json"},
		Settings:     &marker{"settings", present.Traits{Roots: homeFile, Channel: present.ChannelFile}, "settings.json"},
		Hooks:        &marker{"settings-hooks", present.Traits{Roots: homeFile, Channel: present.ChannelFile}, "hooks.json"},
		Commands:     &marker{"commands-dir", present.Traits{Roots: homeFile, Channel: present.ChannelFile}, "commands/.marker"},
		Skills:       &marker{"skills-dir", present.Traits{Roots: homeFile, Channel: present.ChannelFile}, "skills/.marker"},
		CLI: []engine.CLIGrammar{
			{Mode: engine.Interactive, Binary: "mock", Flags: []engine.Flag{{Name: "--resume", HasValue: true}}},
			{Mode: engine.Structured, Binary: "mock", Flags: []engine.Flag{{Name: "--resume", HasValue: true}}},
		},
		ModelAliases: map[string]string{},
		ExportSchema: []byte(`{"type":"object"}`),
	}
	m := Mock{Base: engine.Base{Definition: d}}
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
	return m, nil
}

// Home: mock keeps NO engine-global config or credential state — a bare
// echo compiled into ctxloom that never spawns a grandchild and never
// touches disk — so the null object: nothing relocates, nothing seeds.
func (m Mock) Home() engine.HomeSpec { return engine.HomeSpec{} }

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

// Hooks: mock fires no hooks, so its codec refuses — unreachable, since no
// payload arrives.
func (m Mock) Hooks() engine.HookCodec { return noHooks{m.Name} }

type noHooks struct{ name engine.Name }

func (n noHooks) Decode(string, []byte) (engine.HookEvent, error) {
	return engine.HookEvent{}, engine.ErrUnsupported{Engine: n.name, Capability: "hooks"}
}

// Instance is where REQUIREDNESS is checked, loudly: the mock cannot run a
// session without a context surface to carry the system prompt.
func (m Mock) Instance(s engine.Session) (engine.Instance, error) {
	if m.Context == nil {
		return nil, engine.ErrUnsupported{Engine: m.Name, Capability: "context"}
	}
	return &instance{s: s}, nil
}

type instance struct {
	s   engine.Session
	key string
}

func (i *instance) Exec(_ []present.Presentation) (engine.Exec, error) {
	env := map[string]string{}
	for _, h := range i.s.Home {
		env[h.Var] = h.Path
	}
	args := []string{}
	if i.key != "" {
		args = append(args, "--resume", i.key)
	}
	return engine.Exec{Binary: "mock", Args: args, Env: env, WorkDir: i.s.WorkDir, Interactive: i.s.Mode == engine.Interactive}, nil
}
func (i *instance) Drivers() []engine.StructuredDriver { return []engine.StructuredDriver{driver{}} }
func (i *instance) Resume(key string) error            { i.key = key; return nil }

type driver struct{}

func (driver) Turn(_ context.Context, _ engine.Exec, in engine.Turn, _ chan<- engine.Event) (engine.TurnResult, error) {
	return engine.TurnResult{NativeKey: "mock-session", Answer: in.Prompt}, nil
}

// marker is an observable no-op approach for every kind: it writes an
// empty file at rel under the root the plan selected. One type satisfies
// all six per-kind interfaces so the mock can fill every field.
type marker struct {
	name   string
	traits present.Traits
	rel    string
}

func (a *marker) Name() string           { return a.name }
func (a *marker) Traits() present.Traits { return a.traits }
func (a *marker) write(start present.Start, root present.RootKind, fs afero.Fs) (present.Delivered, error) {
	var r present.Rooted
	if root == present.RootProjectRoot {
		r = start.UnderProjectRoot(a.rel)
	} else {
		r = start.UnderEngineHome(a.rel)
	}
	p := r.Build()
	if err := fs.MkdirAll(filepath.Dir(p.HostPath), 0o700); err != nil {
		return present.Delivered{}, err
	}
	if err := iox.WriteFileAtomicFs(fs, p.HostPath, nil, 0o600); err != nil {
		return present.Delivered{}, err
	}
	return present.Delivered{Presented: p, Wrote: []string{p.HostPath}, Undo: func(fs afero.Fs) error { return fs.Remove(p.HostPath) }}, nil
}
func (a *marker) DeliverContext(s present.Start, r present.RootKind, _ engine.ContextInputs, fs afero.Fs) (present.Delivered, error) {
	return a.write(s, r, fs)
}
func (a *marker) DeliverMCP(s present.Start, r present.RootKind, _ engine.MCPInputs, fs afero.Fs) (present.Delivered, error) {
	return a.write(s, r, fs)
}
func (a *marker) DeliverSettings(s present.Start, r present.RootKind, _ engine.SettingsInputs, fs afero.Fs) (present.Delivered, error) {
	return a.write(s, r, fs)
}
func (a *marker) DeliverHooks(s present.Start, r present.RootKind, _ engine.HooksInputs, fs afero.Fs) (present.Delivered, error) {
	return a.write(s, r, fs)
}
func (a *marker) DeliverCommands(s present.Start, r present.RootKind, _ engine.CommandsInputs, fs afero.Fs) (present.Delivered, error) {
	return a.write(s, r, fs)
}
func (a *marker) DeliverSkills(s present.Start, r present.RootKind, _ engine.SkillsInputs, fs afero.Fs) (present.Delivered, error) {
	return a.write(s, r, fs)
}

// Exports exports EVERYTHING: no bundle carries a block for a mock (mock is
// a test engine nobody publishes a bundle FOR), so there is no opt-out to
// read and nothing to invent one from — a mock that silently exported
// nothing would be a commands surface that reports success and writes zero
// bytes, precisely the silent no-op the mock exists to catch in others. The
// NoSkills double exports no skill: that absence is the subject of every
// missing-skills-surface arm.
func (m Mock) Exports(items engine.Items) (engine.Exports, error) {
	var out engine.Exports
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
