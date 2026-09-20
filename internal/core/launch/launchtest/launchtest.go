// Package launchtest is the fixture for launch.Resolve's design-by-test body
// (docs/architecture/audit-2026-09-18/30-decided-architecture.md, Part 4.2
// C): a Deps built from an in-memory config generation, a fixture engine
// kind, a fake Cells, a sequence endpoint minter and a MemStore. It lives in
// core so the launch tests need no adapter; every port on Deps is a double
// here and a real adapter in production.
package launchtest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
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
	cells    *cells
	asm      *assembler
}

// Assembled is the package the assembler double composed on the most
// recent Resolve: what the carrier on that launch encodes.
func (e Env) Assembled() composite.Package { return e.asm.last }

// Option adjusts the fixture before Deps builds it.
type Option func(*fixture)

// AgentOption adjusts one agent binding the fixture declares.
type AgentOption func(*agentDecl)

type agentDecl struct {
	binding      agents.Agent
	noStructured bool
}

type fixture struct {
	agents    map[string]agentDecl
	available map[launch.RuntimeAxis]bool
	// projectRuntime and projectPermissions are the project-level defaults
	// (config.yaml's `runtime:` and `permissions:`).
	projectRuntime     string
	projectPermissions string
	// projectDirtyTree is the project-level `dirty_tree_handler:` default.
	projectDirtyTree string
	// profileLLM is the label the "base" profile declares, reported by the
	// assembler double.
	profileLLM string
	// noReadOnlyPlan makes the fixture engine declare no read-only tier, so
	// a declared plan collapses to default.
	noReadOnlyPlan bool
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
func EngineWithoutContainer() Option {
	return func(f *fixture) { f.withoutContainer = true }
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

// ProjectRuntime sets the project's `runtime:` default, unparsed.
func ProjectRuntime(s string) Option { return func(f *fixture) { f.projectRuntime = s } }

// ProjectPermissions sets the project's `permissions:` default.
func ProjectPermissions(s string) Option { return func(f *fixture) { f.projectPermissions = s } }

// ProjectDirtyTree sets the project's `dirty_tree_handler:` default, unparsed.
func ProjectDirtyTree(s string) Option { return func(f *fixture) { f.projectDirtyTree = s } }

// NoReadOnlyPlan makes the fixture engine declare no read-only tier.
func NoReadOnlyPlan() Option { return func(f *fixture) { f.noReadOnlyPlan = true } }

// ProfileLLM makes the composed profiles declare a label.
func ProfileLLM(label string) Option { return func(f *fixture) { f.profileLLM = label } }

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
				"guarded": {Type: string(EngineName), Permissions: "plan"},
			},
			Defaults: config.RoleDefaults{Primary: "primary", Fast: "fast"},
		},
		Agents:           bindings,
		DefaultAgent:     "setup",
		Runtime:          f.projectRuntime,
		Permissions:      f.projectPermissions,
		DirtyTreeHandler: f.projectDirtyTree,
	})
	snap := &config.Snapshot{Config: cfg, Trust: composite.Trust{}}

	modes := []engine.Mode{engine.Interactive, engine.Structured}
	if f.noStructured {
		modes = []engine.Mode{engine.Interactive}
	}
	reg, err := engine.NewRegistry(newFixtureEngine(modes, !f.noReadOnlyPlan))
	require.NoError(t, err)

	store := sessions.NewMemStore()
	entry, err := store.AssignHarp(project, "")
	require.NoError(t, err)

	c := &cells{available: f.available, withoutContainer: f.withoutContainer}
	asm := &assembler{profileLLM: f.profileLLM}
	return Env{
		cells: c,
		asm:   asm,
		Deps: launch.Deps{
			Snapshot:   snap,
			Engines:    reg,
			Assembler:  asm,
			Cells:      c,
			Endpoints:  &StableMinter{},
			Sessions:   store,
			Inline:     composite.Inline{Max: composite.DefaultInlineMax},
			ClaimCheck: composite.ClaimCheck{Store: MemStore{}},
			InlineMax:  composite.DefaultInlineMax,
			Host:       launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
		},
		Identity: sessions.Identity{Harp: entry.HarpName, Project: "proj"},
		Project:  project,
	}
}

// LastCellRequest is the request the resolver handed the cells port on its
// most recent Prepare: what the cell was asked for, as settled by Resolve.
func (e Env) LastCellRequest() launch.CellRequest { return e.cells.last }

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

// EngineName is the fixture engine's registry name: not a shipped engine's,
// so no test can pass by naming one.
const EngineName engine.Name = "fixture"

// fixtureEngine is the kind the fixture registers: every surface carried by
// a file approach rooted at the session home, both modes unless the option
// drops Structured.
type fixtureEngine struct{ engine.Base }

func newFixtureEngine(modes []engine.Mode, readOnlyPlan bool) engine.Engine {
	a := &approach{}
	d := engine.Definition{
		Name:         EngineName,
		Distribution: engine.DistributionDefault,
		Modes:        modes,
		Permissions:  engine.PermissionFacts{ReadOnlyPlan: readOnlyPlan, HostDefault: engine.PermissionDefault},
		ModelAliases: map[string]string{"fast-model": "fixture-fast-2"},
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

// Exports exports every item as-is: the fixture decodes no block.
func (fixtureEngine) Home() engine.HomeSpec { return engine.HomeSpec{} }
func (e fixtureEngine) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{}, engine.ErrUnsupported{Engine: e.Name, Capability: "container"}
}
func (fixtureEngine) Transcripts() []engine.TranscriptReader { return nil }
func (fixtureEngine) Hooks() engine.HookCodec                { return nil }

func (fixtureEngine) Exports(items engine.Items) (engine.Exports, error) {
	var out engine.Exports
	for _, c := range items.Commands {
		out.Commands = append(out.Commands, engine.CommandExport{Name: c.Name, Body: c.Body, Enabled: true, Description: c.Description})
	}
	for _, s := range items.Skills {
		out.Skills = append(out.Skills, engine.SkillExport{Name: s.Name, Description: s.Description, Files: s.Files, Enabled: true})
	}
	return out, nil
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
// joined, the surfaces are empty, the profiles declare the fixture's label
// (none by default); the catalog index is empty.
type assembler struct {
	profileLLM string
	last       composite.Package
}

func (a *assembler) Assemble(_ context.Context, _ *config.Snapshot, sel launch.Selection) (composite.Package, error) {
	text := fmt.Sprintf("context of %v", sel.Profiles)
	a.last = composite.Package{
		Context:   composite.Context{Text: text, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))},
		Selection: composite.Selection{Profiles: sel.Profiles, LLM: a.profileLLM},
	}
	return a.last, nil
}

func (*assembler) Index(context.Context, *config.Snapshot) (composite.Index, error) {
	return composite.Index{}, nil
}

func (*assembler) LabelEnv(*config.Snapshot, string) map[string]string { return nil }

// MemStore is the in-memory claim store: the content-addressed location is
// the digest itself.
type MemStore map[string][]byte

func (m MemStore) Put(_ context.Context, digest [32]byte, b []byte) (string, error) {
	m[string(digest[:])] = b
	return string(digest[:]), nil
}
func (m MemStore) Get(_ context.Context, loc string) ([]byte, error) { return m[loc], nil }

// Redeem is the consumer's shape conditional, mirrored from Resolve's size
// conditional: a claim present names the claim check, otherwise the inline
// transport.
func Redeem(ctx context.Context, inline, claim composite.Transport, c composite.Carrier) (composite.Encoded, error) {
	return composite.Redeem(ctx, inline, claim, c)
}

// cells is the Cells double: the host cell is the project root; a container
// axis is refused as an ownership mismatch when its runtime is not
// available and as the engine's own refusal when the engine has no
// container story.
type cells struct {
	available        map[launch.RuntimeAxis]bool
	withoutContainer bool
	last             launch.CellRequest
}

func (c *cells) Prepare(_ context.Context, req launch.CellRequest) (launch.Cell, error) {
	c.last = req
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
