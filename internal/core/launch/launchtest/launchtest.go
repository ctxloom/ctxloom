// Package launchtest is the fixture for launch.Resolve's design-by-test body
// (docs/architecture/audit-2026-09-18/30-decided-architecture.md, Part 4.2
// C): a Deps built from an in-memory config generation, a fixture engine
// kind, a fake Cells, a sequence endpoint minter and a MemStore. It lives in
// core so the launch tests need no adapter; every port on Deps is a double
// here and a real adapter in production.
package launchtest

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Env is what Deps hands a test: the ports, the identity the test minted
// (the caller mints; Resolve never does) and the project root.
type Env struct {
	Deps     launch.Deps
	Identity sessions.Identity
	Project  string
}

// Option adjusts the fixture before Deps builds it.
type Option func(*fixture)

// AgentOption adjusts one agent binding the fixture declares.
type AgentOption func(*agentDecl)

type agentDecl struct {
	binding        agents.Agent
	noStructured   bool
	engineWithout  bool
	engineNameUsed engine.Name
}

type fixture struct {
	agents    map[string]agentDecl
	available map[launch.RuntimeAxis]bool
	// noStructured drops Structured from the fixture engine's Modes.
	noStructured bool
	// withoutContainer makes the fake Cells refuse a container axis with
	// the engine's own ErrUnsupported, the way Engine.Container() will.
	withoutContainer bool
}

// WithAgent declares an agent binding named name, composed over the "base"
// profile and selecting the "primary" label unless an option says otherwise.
func WithAgent(name string, opts ...AgentOption) Option {
	return func(f *fixture) {
		d := agentDecl{binding: agents.Agent{Name: name, Profiles: []string{"base"}}}
		for _, o := range opts {
			o(&d)
		}
		if d.noStructured {
			f.noStructured = true
		}
		if d.engineWithout {
			f.withoutContainer = true
		}
		f.agents[name] = d
	}
}

// Runtime sets the binding's runtime axis.
func Runtime(r launch.RuntimeAxis) AgentOption {
	return func(d *agentDecl) { d.binding.Runtime = string(r) }
}

// Permissions sets the binding's declared permission posture.
func Permissions(p string) AgentOption {
	return func(d *agentDecl) { d.binding.Permissions = p }
}

// NoStructuredDrive makes the fixture engine declare Interactive only, so a
// Structured Source is refused at Definition.Modes.
func NoStructuredDrive() AgentOption {
	return func(d *agentDecl) { d.noStructured = true }
}

// EngineWithoutContainer makes the engine refuse a container cell with its
// own ErrUnsupported{Capability: "container"}.
func EngineWithoutContainer() AgentOption {
	return func(d *agentDecl) { d.engineWithout = true }
}

// RuntimesAvailable names the container runtimes the fake Cells can reach;
// a binding asking for another ownership mode is an ErrOwnershipMismatch.
func RuntimesAvailable(axes ...launch.RuntimeAxis) Option {
	return func(f *fixture) {
		f.available = map[launch.RuntimeAxis]bool{}
		for _, a := range axes {
			f.available[a] = true
		}
	}
}

// Deps builds the fixture: a config generation declaring the "primary" and
// "fast" labels on the fixture engine, the internal bindings every launch
// path names ("setup" for init's probe, "distiller" for the distill
// one-shot), plus whatever WithAgent added; an identity minted from the
// MemStore; and a fake for every port.
func Deps(t *testing.T, opts ...Option) Env {
	t.Helper()
	f := &fixture{agents: map[string]agentDecl{}, available: map[launch.RuntimeAxis]bool{launch.RuntimeHost: true}}
	for _, o := range opts {
		o(f)
	}
	project := t.TempDir()

	bindings := map[string]agents.Agent{
		"setup":     {Name: "setup", Profiles: []string{"base"}},
		"distiller": {Name: "distiller", Profiles: []string{"base"}, LLM: "fast"},
	}
	for name, d := range f.agents {
		bindings[name] = d.binding
	}
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"primary": {Type: string(EngineName)},
				"fast":    {Type: string(EngineName), Body: map[string]any{"model": "fast-model"}},
			},
			Defaults: config.RoleDefaults{Primary: "primary", Fast: "fast"},
		},
		Agents:       bindings,
		DefaultAgent: "setup",
	})
	snap := &config.Snapshot{Config: cfg, Trust: composite.Trust{}}

	modes := []engine.Mode{engine.Interactive, engine.Structured}
	if f.noStructured {
		modes = []engine.Mode{engine.Interactive}
	}
	reg, err := engine.NewRegistry(newFixtureEngine(modes))
	require.NoError(t, err)

	store := sessions.NewMemStore()
	entry, err := store.AssignHarp(project, "")
	require.NoError(t, err)

	return Env{
		Deps: launch.Deps{
			Snapshot:  snap,
			Engines:   reg,
			Assembler: assembler{},
			Cells:     &cells{available: f.available, withoutContainer: f.withoutContainer},
			Endpoints: &StableMinter{},
			Sessions:  store,
			Host:      launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
		},
		Identity: sessions.Identity{Harp: entry.HarpName, Project: "proj"},
		Project:  project,
	}
}

// Expect is what one Source resolves to.
type Expect struct {
	Engine     engine.Name
	Label      string
	Permission engine.PermissionMode
	Axes       launch.Axes
}

// Assert checks the launch against the expectation; a zero Axes is not
// checked (the table lists it only where the axes are the point).
func (e Expect) Assert(t *testing.T, l launch.Launch) {
	t.Helper()
	require.Equal(t, e.Engine, l.Engine)
	require.Equal(t, e.Label, l.Label.Label)
	require.Equal(t, e.Permission, l.Permission)
	if e.Axes != (launch.Axes{}) {
		require.Equal(t, e.Axes, l.Axes)
	}
}

// StableMinter mints a deterministic sequence of loopback endpoints: each
// call is a fresh address, so a reuse observed across two Resolve calls came
// from the session record and a rebind visibly moved.
type StableMinter struct{ n int }

// MintMCP mints the next endpoint in the sequence.
func (m *StableMinter) MintMCP(_ context.Context, id sessions.Identity, _ launch.Axes) (sessions.Endpoint, error) {
	m.n++
	return sessions.Endpoint{URL: "http://127.0.0.1:" + strconv.Itoa(40000+m.n) + "/mcp", Credential: "bearer-" + id.Harp + "-" + strconv.Itoa(m.n)}, nil
}

// EngineName is the fixture engine's registry name.
const EngineName engine.Name = "mock"

// fixtureEngine is the kind the fixture registers: every surface carried by
// a file approach rooted at the session home, both modes unless the option
// drops Structured.
type fixtureEngine struct{ engine.Base }

func newFixtureEngine(modes []engine.Mode) engine.Engine {
	a := &approach{}
	d := engine.Definition{
		Name:         EngineName,
		Distribution: engine.DistributionDefault,
		Modes:        modes,
		Permissions:  engine.PermissionFacts{ReadOnlyPlan: true, HostDefault: engine.PermissionDefault},
		Context:      a, MCP: a, Settings: a, Hooks: a, Commands: a, Skills: a,
	}
	for _, m := range modes {
		d.CLI = append(d.CLI, engine.CLIGrammar{Mode: m, Binary: "fixture", Positional: 1})
	}
	e := fixtureEngine{engine.Base{Definition: d}}
	if err := e.Validate(); err != nil {
		panic(err)
	}
	return e
}

func (fixtureEngine) Instance(engine.Session) (engine.Instance, error) {
	return nil, engine.ErrUnsupported{Engine: EngineName, Capability: "instance"}
}

// approach is the fixture's one typed approach for every kind: a file under
// the session home.
type approach struct{}

func (*approach) Name() string { return "fixture-file" }
func (*approach) Traits() present.Traits {
	return present.Traits{Roots: []present.RootKind{present.RootSessionHome, present.RootProjectRoot}, Channel: present.ChannelFile}
}
func (*approach) DeliverContext(present.Start, present.RootKind, engine.ContextInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*approach) DeliverMCP(present.Start, present.RootKind, engine.MCPInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*approach) DeliverSettings(present.Start, present.RootKind, engine.SettingsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*approach) DeliverHooks(present.Start, present.RootKind, engine.HooksInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*approach) DeliverCommands(present.Start, present.RootKind, engine.CommandsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}
func (*approach) DeliverSkills(present.Start, present.RootKind, engine.SkillsInputs, afero.Fs) (present.Delivered, error) {
	return present.Delivered{}, nil
}

// assembler is the Assembler double: the context is the profile set's names
// joined, the managed surfaces are empty, the profiles declare no llm.
type assembler struct{}

func (assembler) Assemble(_ context.Context, _ *config.Snapshot, profiles []string) (launch.Assembled, error) {
	return launch.Assembled{Context: fmt.Sprintf("context of %v", profiles), Profiles: profiles}, nil
}

func (assembler) Surfaces(_ context.Context, _ *config.Snapshot, _ engine.Name, _ string, _ []string) (*agent.ManagedConfig, error) {
	return &agent.ManagedConfig{}, nil
}

// cells is the Cells double: the host cell is the project root; a container
// axis is refused as an ownership mismatch when its runtime is not
// available and as the engine's own refusal when the engine has no
// container story.
type cells struct {
	available        map[launch.RuntimeAxis]bool
	withoutContainer bool
}

func (c *cells) Prepare(_ context.Context, req launch.CellRequest) (launch.Cell, error) {
	if req.Axes.WantsContainer() {
		if !c.available[req.Axes.Runtime] {
			return launch.Cell{}, launch.ErrOwnershipMismatch
		}
		if c.withoutContainer {
			return launch.Cell{}, engine.ErrUnsupported{Engine: req.Engine.Root().Name, Capability: "container"}
		}
	}
	paths := present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: req.ProjectRoot},
		CtxloomHome: present.Root{Host: req.Host.CtxloomHome},
		Scratch:     present.Root{Host: req.SessionDir},
	})
	return launch.Cell{Paths: paths, Workspace: req.ProjectRoot, Cleanup: func() error { return nil }}, nil
}
