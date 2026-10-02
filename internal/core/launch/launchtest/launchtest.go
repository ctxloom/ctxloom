// Package launchtest is the fixture for launch.Resolve's design-by-test body:
// a Deps built from an in-memory config generation, a fixture engine
// kind, a fake Cells, a sequence endpoint minter and a MemStore. It lives in
// core so the launch tests need no adapter; every port on Deps is a double
// here and a real adapter in production.
package launchtest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
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
	projectPermissions agents.NeutralPermissions
	// labelPermissions is the "guarded" label's permissions block.
	labelPermissions agents.LabelPermissions
	// noApprovals makes the fixture engine declare no approval codec.
	noApprovals bool
	// noPermissionModel makes the fixture engine declare no permission model.
	noPermissionModel bool
	// reviewer makes the fixture's permission model serve a reviewer.
	reviewer bool
	// projectDirtyTree is the project-level `dirty_tree_handler:` default.
	projectDirtyTree string
	// profileLLM is the label the "base" profile declares, reported by the
	// assembler double.
	profileLLM string
	// noStructured drops Structured from the fixture engine's Modes.
	noStructured bool
	// relocatableHome makes the fixture engine declare a relocatable home
	// (one home var), so the cell advises an engine home for a session-home
	// run and none for a host-home run.
	relocatableHome bool
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

// Permissions sets the binding's declared permission mode.
func Permissions(p string) AgentOption {
	return func(d *agentDecl) {
		d.binding.Permissions = agents.Permissions{Engines: map[string]map[string]any{string(EngineName): {"mode": p}}}
	}
}

// PermissionBlock sets the binding's whole permissions block.
func PermissionBlock(p agents.Permissions) AgentOption {
	return func(d *agentDecl) { d.binding.Permissions = p }
}

// EngineHome sets the binding's `engine_home:` declaration, unparsed.
func EngineHome(s string) AgentOption {
	return func(d *agentDecl) { d.binding.HomeMode = s }
}

// Auth sets the binding's `auth:` declaration, unparsed.
func Auth(s string) AgentOption {
	return func(d *agentDecl) { d.binding.Auth = s }
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

// ProjectPermissionBlock sets the project's whole `permissions:` block.
func ProjectPermissionBlock(p agents.NeutralPermissions) Option {
	return func(f *fixture) { f.projectPermissions = p }
}

// GuardedLabelPermissions replaces the "guarded" label's permissions block
// (mode plan unless this says otherwise).
func GuardedLabelPermissions(p agents.LabelPermissions) Option {
	return func(f *fixture) { f.labelPermissions = p }
}

// NoPermissionModel makes the fixture engine declare no permission model.
func NoPermissionModel() Option { return func(f *fixture) { f.noPermissionModel = true } }

// FixtureReviewer makes the fixture's permission model serve approver:
// reviewer.
func FixtureReviewer() Option { return func(f *fixture) { f.reviewer = true } }

// NoApprovals makes the fixture engine declare no approval codec.
func NoApprovals() Option { return func(f *fixture) { f.noApprovals = true } }

// ProjectDirtyTree sets the project's `dirty_tree_handler:` default, unparsed.
func ProjectDirtyTree(s string) Option { return func(f *fixture) { f.projectDirtyTree = s } }

// RelocatableHome makes the fixture engine declare a relocatable home, the
// way an engine with its own config dir does: a session-home run advises
// it, a host-home run advises none.
func RelocatableHome() Option { return func(f *fixture) { f.relocatableHome = true } }

// ProfileLLM makes the composed profiles declare a label.
func ProfileLLM(label string) Option { return func(f *fixture) { f.profileLLM = label } }

// Deps builds the fixture: a config generation declaring the "primary" and
// "fast" labels on the fixture engine, the internal bindings every launch
// path names ("setup" for init's probe, "distiller" for the distill
// one-shot), plus whatever WithAgent added; an identity minted from the
// MemStore; and a fake for every port.
func Deps(t *testing.T, opts ...Option) Env {
	t.Helper()
	f := &fixture{agents: map[string]agentDecl{}, available: map[launch.RuntimeAxis]bool{launch.RuntimeHost: true}, labelPermissions: agents.LabelPermissions{Engine: map[string]any{"mode": "plan"}}}
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
				"guarded": {Type: string(EngineName), Permissions: f.labelPermissions},
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
	var model *FixtureModel
	if !f.noPermissionModel {
		model = &FixtureModel{HasReviewer: f.reviewer}
	}
	reg, err := engine.NewRegistry(newFixtureEngine(modes, f.relocatableHome, !f.noApprovals, model))
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
	Permission string // the fixture engine's resolved mode
	Axes       launch.Axes
}

// ModeOf is the launch's resolved mode, as the fixture engine (and every
// shipped one) writes it: its posture document's "mode".
func ModeOf(l launch.Launch) string {
	m, _ := l.Permission.Posture.Document["mode"].(string)
	return m
}

// Assert checks the launch against the expectation; a zero Axes is not
// checked (the table lists it only where the axes are the point).
func (e Expect) Assert(t *testing.T, l launch.Launch) {
	t.Helper()
	require.Equal(t, e.Engine, l.Engine)
	require.Equal(t, e.Label, l.Label.Label)
	require.Equal(t, EngineName, l.Permission.Posture.Engine)
	require.Equal(t, e.Permission, ModeOf(l))
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
type fixtureEngine struct {
	engine.Base
	home        engine.HomeSpec
	approvals   engine.Declared[engine.ApprovalCodec]
	permissions engine.Declared[engine.PermissionModel]
}

// model nil declares no permission model.
func newFixtureEngine(modes []engine.Mode, relocatableHome, approvals bool, model *FixtureModel) engine.Engine {
	a := &approach{}
	d := engine.Definition{
		Name:         EngineName,
		Distribution: engine.DistributionDefault,
		Modes:        modes,
		ModelAliases: map[string]string{"fast-model": "fixture-fast-2"},
		Context:      a, MCP: a, Settings: a, Hooks: a, Commands: a, Skills: a,
	}
	for _, m := range modes {
		d.CLI = append(d.CLI, engine.CLIGrammar{Mode: m, Binary: "fixture", Positional: 1})
	}
	e := fixtureEngine{Base: engine.Base{Definition: d}, approvals: engine.Absent[engine.ApprovalCodec]("NoApprovals: the fixture declares no codec"),
		permissions: engine.Absent[engine.PermissionModel]("NoPermissionModel: the fixture declares none")}
	if model != nil {
		e.permissions = engine.Provide[engine.PermissionModel](*model)
	}
	if approvals {
		e.approvals = engine.Provide[engine.ApprovalCodec](fixtureCodec{})
	}
	if relocatableHome {
		e.home = engine.HomeSpec{Vars: []engine.HomeVar{{Name: "FIXTURE_HOME", Subdir: ".fixture"}}, Auth: engine.Absent[engine.Auth]("the fixture authenticates against no vendor")}
		if err := e.home.Validate(); err != nil {
			panic(err)
		}
	}
	if err := e.Validate(); err != nil {
		panic(err)
	}
	return e
}

func (fixtureEngine) Instance(engine.Session) (engine.Instance, error) {
	return nil, engine.ErrUnsupported{Engine: EngineName, Capability: "instance"}
}

// Home is the zero spec unless RelocatableHome declared one.
func (e fixtureEngine) Home() engine.HomeSpec { return e.home }
func (e fixtureEngine) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{}, engine.ErrUnsupported{Engine: e.Name, Capability: "container"}
}
func (fixtureEngine) Transcripts() []engine.TranscriptReader { return nil }
func (fixtureEngine) Hooks() engine.HookCodec                { return nil }
func (fixtureEngine) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Absent[engine.WakeSpec]("a test double wakes nothing")
}

// Approvals is the fixture codec unless NoApprovals declared it absent.
func (e fixtureEngine) Approvals() engine.Declared[engine.ApprovalCodec] { return e.approvals }

// fixtureCodec validates rules only: a rule starting with "!" is not one.
type fixtureCodec struct{}

// ErrFixtureRule is the fixture codec's refusal of a rule.
var ErrFixtureRule = errors.New("fixture: not a rule")

func (fixtureCodec) DecodeAsk(string, []byte) (engine.PermissionAsk, error) {
	return engine.PermissionAsk{}, engine.ErrUnsupported{Engine: EngineName, Capability: "approvals"}
}

// Permissions is FixtureModel unless NoPermissionModel declared it absent.
func (e fixtureEngine) Permissions() engine.Declared[engine.PermissionModel] { return e.permissions }
func (fixtureCodec) EncodeAnswer(string, engine.PermissionAsk, engine.PermissionAnswer) ([]byte, error) {
	return nil, engine.ErrUnsupported{Engine: EngineName, Capability: "approvals"}
}
func (fixtureCodec) RepoSurfaces() []string { return nil }

// Hooks is one approval hook on the fixture's own ask event.
func (fixtureCodec) Hooks(timeout time.Duration) wire.UnifiedHooks {
	return wire.UnifiedHooks{PermissionAsk: []wire.Hook{agent.ApprovalHook("fixture_ask", "", timeout)}}
}
func (fixtureCodec) ValidateRule(rule string) error {
	if strings.HasPrefix(rule, "!") {
		return ErrFixtureRule
	}
	return nil
}

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
	homeMode, err := agents.ParseHomeMode(string(req.HomeMode))
	if err != nil {
		return launch.Cell{}, err
	}
	roots := present.Paths{ProjectRoot: present.Root{Host: req.ProjectRoot}}
	// The session home is the production rule's, never for a host-home run:
	// the real home is the engine's own, not ours to deliver into.
	if dir, ok := launch.SessionHome(req.SessionDir, req.Engine, homeMode); ok {
		roots.SessionHome = present.Root{Host: dir}
	}
	return launch.Cell{Placement: launch.Placement{Paths: present.OnHost(roots)}, Workspace: req.ProjectRoot, HomeMode: req.HomeMode, Cleanup: func() error { return nil }}, nil
}

// Structured is a resolved structured-mode launch for one harp on the
// host, carrying an empty package inline: the fixture a test hands a runner
// half (or a coordinator's spawner double) when the launch's contents are
// not what the test is about.
// perm is the posture's mode, in backend's own vocabulary.
func Structured(harp, backend, label, model, workDir, perm string) launch.Launch {
	enc, err := composite.Encode(composite.Package{})
	if err != nil {
		panic(err)
	}
	carrier, err := composite.Inline{}.Carry(context.Background(), enc)
	if err != nil {
		panic(err)
	}
	return launch.Launch{
		Identity:   sessions.Identity{Harp: harp},
		Engine:     engine.Name(backend),
		Label:      engine.LabelConfig{Label: label, Model: model},
		Mode:       engine.Structured,
		Permission: engine.PermissionPolicy{Posture: engine.Posture{Engine: engine.Name(backend), Document: map[string]any{"mode": perm}}, Sandbox: engine.SandboxFull},
		Cell:       launch.Cell{Placement: launch.Placement{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: workDir}})}, Workspace: workDir, Cleanup: func() error { return nil }},
		Package:    carrier,
	}
}
