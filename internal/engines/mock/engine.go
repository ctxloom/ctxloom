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
// its typed approaches, exactly as Base.Delegate decides. The mock BACKEND
// that runs today's launches (lm/backends) is a separate value paired with
// this kind by name at the composition root.

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
}

// Option adjusts the Definition before Validate.
type Option func(*engine.Definition)

// Without nils the typed field for each kind: the lossy variant the
// uncarried and requiredness tests use.
func Without(kinds ...present.Kind) Option {
	return func(d *engine.Definition) {
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
	return func(def *engine.Definition) { def.Distribution = d }
}

// WithDynamic provides a dynamic approach (the delegation tests use it).
func WithDynamic() Option {
	return func(d *engine.Definition) { d.Dynamic = &endpointEntry{name: "session-endpoint"} }
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
	return func(d *engine.Definition) {
		var keep []engine.CLIGrammar
		for _, g := range d.CLI {
			if g.Mode != mode {
				keep = append(keep, g)
			}
		}
		d.CLI = keep
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
// composition root registers.
func Doubles() []engine.Engine {
	return []engine.Engine{
		New(),
		NewNamed(NameLossy),
		NewNamed(NameLaunch, Without(present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills)),
		NewNoSkills(),
	}
}

// NewNoSkills is the double that exports no skill: THE ABSENCE IS THE
// ENTIRE POINT. Every other kind exports skills, which would leave the
// missing-skills arm of every caller with nothing to point at; it is a
// declared difference, so it cannot be "completed" by accident.
func NewNoSkills() engine.Engine {
	m := NewNamed(NameNoSkills).(Mock)
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
	for _, o := range opts {
		o(&d)
	}
	b := engine.Base{Definition: d}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return Mock{Base: b}, nil
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
