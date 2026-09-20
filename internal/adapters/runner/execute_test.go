package runner_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// TestExecute_HostAndDelegatedLaunches_DeliverAnIdenticalFileSet is the
// gate: a host `run --agent x` and an `agent_run x` over the SAME binding
// deliver the same set of files (relative to their roots, byte for byte).
// The host arm delivers the local launcher's Loadout (Launch.Loadout over
// the opened package) through the static writer into its own cell; the
// delegated arm delivers through runner.Execute over the launch that rode
// the wire. Both resolve through launch.Resolve; only WHO asks (depth,
// mode, workspace) differs.
func TestExecute_HostAndDelegatedLaunches_DeliverAnIdenticalFileSet(t *testing.T) {
	env := newDeliveryEnv(t)

	host, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 0, ""),
		Agent:    "x", Mode: engine.Interactive, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	child, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-1"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project, Workspace: launch.WorkspaceWorktree,
	})
	require.NoError(t, err)
	require.NotEqual(t, host.Cell.Workspace, child.Cell.Workspace, "the child runs in its own worktree cell")

	// The host arm: the local launcher opens the package and delivers the
	// Launch's Loadout through the static writer into its own cell.
	opened, err := operations.OpenLaunch(context.Background(), env.deps, host)
	require.NoError(t, err)
	static, rec := staticWriter(t), records(t)
	_, err = static.Deliver(context.Background(), opened.Loadout, mock.New().Root().Surfaces(), host.Target(rec))
	require.NoError(t, err)

	// The delegated arm: the launch crosses the wire and the runner delivers.
	drive := &recordingDriver{}
	wire, err := coordgrpc.DecodeLaunch(coordgrpc.EncodeLaunch(child))
	require.NoError(t, err)
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind:   mock.New(),
		Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: static, Records: rec, Driver: drive,
	}, wire)
	require.NoError(t, err)

	hostSet := cellTree(t, host)
	childSet := cellTree(t, child)
	require.NotEmpty(t, hostSet, "the host arm delivered nothing — the fixture carries no surfaces")
	require.Equal(t, hostSet, childSet, "a host launch and a delegated launch over one binding deliver one file set")
	t.Logf("delivered (both arms):\n%s", strings.Join(keys(hostSet), "\n"))

	// The delegated arm's drive: the engine is driven in the child's cell,
	// at the floored posture, with the assembled context leading the first
	// turn and the session's .mcp.json naming the composed servers.
	require.Len(t, drive.turns, 1)
	turn := drive.turns[0]
	require.Equal(t, child.Cell.Workspace, turn.Chat.WorkDir)
	require.Equal(t, child.Permission, turn.Chat.Permissions)
	require.Equal(t, "run-1", turn.Launch.Identity.RunID)
	require.True(t, strings.HasPrefix(turn.Prompt, opened.Package.Context.Text), "the composed context leads the first turn")
	require.True(t, strings.HasSuffix(turn.Prompt, "go"), "the prompt is the first turn")
	require.FileExists(t, turn.Chat.MCPConfigPath)
	require.True(t, strings.HasPrefix(turn.Chat.MCPConfigPath, child.Cell.Paths.Paths().Scratch.Host), "the MCP file lands under the session's own root, never the project tree")
	body, err := os.ReadFile(turn.Chat.MCPConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(body), `"ctxloom"`, "the composed servers are what .mcp.json names")
	require.Contains(t, string(body), `"deploy-tool"`)
}

// TestExecute_ANativeKeyResumeDoesNotRePrimeTheContext: a resume with the
// engine's native key continues the engine's own recorded session; the
// runner leads with nothing.
func TestExecute_ANativeKeyResumeDoesNotRePrimeTheContext(t *testing.T) {
	env := newDeliveryEnv(t)
	id := env.mint(t, 1, "run-2")
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: id,
		Agent:    "x", Mode: engine.Structured, Prompt: "again", WorkDir: env.project,
		Resume: launch.Resume{Ref: sessions.ResumeRef{Harp: id.Harp, NativeKey: "native-9"}},
	})
	require.NoError(t, err)
	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	require.NoError(t, err)
	require.Equal(t, "again", drive.turns[0].Prompt)
	require.Equal(t, "native-9", drive.turns[0].Chat.ResumeSessionID)
}

// TestExecute_RefusesALaunchForAnotherEngine: the runner hosts ONE engine
// (its RunnerHello advertised it); a launch naming another is refused before
// anything is delivered.
func TestExecute_RefusesALaunchForAnotherEngine(t *testing.T) {
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-3"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: mock.NewNamed("other"), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	require.ErrorIs(t, err, runner.ErrWrongEngine)
	require.Empty(t, drive.turns)
	require.Empty(t, cellTree(t, l), "nothing was delivered")
}

// TestExecute_BindsTheInstanceBeforeDelivery: the runner asks the engine
// KIND for the session's Instance before anything is delivered, so
// requiredness is refused HERE, by the engine, by name — a kind whose
// Definition lacks the context surface the session needs refuses the
// launch and nothing is written or driven.
func TestExecute_BindsTheInstanceBeforeDelivery(t *testing.T) {
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-5"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(mock.Without(present.Context)), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	var unsupported engine.ErrUnsupported
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "context", unsupported.Capability)
	require.Equal(t, engine.Name("mock"), unsupported.Engine)
	require.Empty(t, drive.turns)
	require.Empty(t, cellTree(t, l), "nothing was delivered")
}

// TestExecute_RefusesAStructuredLaunchTheInstanceCannotDrive: a Structured
// launch on an Instance with no driver is refused with
// ErrUnsupported{Capability: "drive"} before delivery.
func TestExecute_RefusesAStructuredLaunchTheInstanceCannotDrive(t *testing.T) {
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-6"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: driverless{mock.New()}, Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	var unsupported engine.ErrUnsupported
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "drive", unsupported.Capability)
	require.Empty(t, drive.turns)
}

// driverless is the mock kind whose instances carry no structured driver.
type driverless struct{ engine.Engine }

func (d driverless) Instance(s engine.Session) (engine.Instance, error) {
	inst, err := d.Engine.Instance(s)
	if err != nil {
		return nil, err
	}
	return noDrivers{inst}, nil
}

type noDrivers struct{ engine.Instance }

func (noDrivers) Drivers() []engine.StructuredDriver { return nil }

// TestExecute_ATamperedClaimIsRefusedBeforeDelivery: a claim whose stored
// bytes were altered never reaches the writers.
func TestExecute_ATamperedClaimIsRefusedBeforeDelivery(t *testing.T) {
	env := newDeliveryEnv(t)
	env.deps.InlineMax = -1
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-4"),
		Agent:    "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	require.NotNil(t, l.Package.Claim)
	env.store[l.Package.Claim.Location] = []byte("tampered")
	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	require.ErrorIs(t, err, composite.ErrDigestMismatch)
	require.ErrorContains(t, err, l.Package.Claim.Location)
	require.Empty(t, drive.turns)
}

// deliveryEnv is the resolver over a config fixture naming the mock engine
// and one binding "x" that composes hooks, commands, skills, servers and a
// deny list — every surface the mock's writers land.
type deliveryEnv struct {
	deps    launch.Deps
	project string
	store   launchtest.MemStore
}

// mint assigns a harp in the store (the caller mints; Resolve never does).
func (e *deliveryEnv) mint(t *testing.T, depth int, runID string) sessions.Identity {
	t.Helper()
	entry, err := e.deps.Sessions.AssignHarp(e.project, "")
	require.NoError(t, err)
	return sessions.Identity{Harp: entry.HarpName, RunID: runID, Depth: depth, Project: "proj"}
}

func newDeliveryEnv(t *testing.T) *deliveryEnv {
	t.Helper()
	project := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"primary": {Type: string(backends.NewMock().Name())}},
			Defaults: config.RoleDefaults{Primary: "primary"},
		},
		Agents:       map[string]agents.Agent{"x": {Name: "x", Profiles: []string{"base"}, Permissions: "bypass"}},
		DefaultAgent: "x",
	})
	store := launchtest.MemStore{}
	return &deliveryEnv{
		project: project,
		store:   store,
		deps: launch.Deps{
			Snapshot:   &config.Snapshot{Config: cfg},
			Engines:    backends.Engines(),
			Assembler:  fixedAssembler{},
			Cells:      &cells{sessions: t.TempDir(), worktrees: t.TempDir()},
			Endpoints:  &launchtest.StableMinter{},
			Sessions:   sessions.NewMemStore(),
			Inline:     composite.Inline{Max: composite.DefaultInlineMax},
			ClaimCheck: composite.ClaimCheck{Store: store},
			InlineMax:  composite.DefaultInlineMax,
			Host:       launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
		},
	}
}

// fixedAssembler composes one package for every profile set: the context,
// one command, one skill, a hook, two servers, a deny list, the statusline.
type fixedAssembler struct{}

func (fixedAssembler) Assemble(_ context.Context, _ *config.Snapshot, sel launch.Selection) (composite.Package, error) {
	text := "# guidance\nrepeat the marker MARKER-7f3a"
	return composite.Package{
		Context: composite.Context{Text: text, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))},
		Commands: []composite.Item[composite.Command]{{Value: composite.Command{
			Name: "review", Bundle: "dev", Item: "review", ExportName: "dev-review", Description: "review it", Body: "Review the diff.",
		}, Ref: "dev#prompts/review"}},
		Skills: []composite.Item[composite.Skill]{{Value: composite.Skill{
			Name: "triage", Bundle: "dev", Item: "triage", Description: "triage it",
			Files: []engine.SkillFile{{Path: "SKILL.md", Size: 12, Mode: 0o644, Bytes: []byte("# triage\nok\n")}},
		}, Ref: "dev#skills/triage"}},
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "echo pre", Type: "command"}}}},
		MCP: map[string]wire.MCPServer{
			"ctxloom":     {Command: "ctxloom", Args: []string{"mcp"}},
			"deploy-tool": {Command: "deploy", Args: []string{"--serve"}},
		},
		DenyTools:  []string{"Task"},
		Statusline: true,
		Selection:  composite.Selection{Profiles: sel.Profiles},
		Loaded:     []string{"dev#fragments/guidance"},
	}, nil
}

func (fixedAssembler) Index(context.Context, *config.Snapshot) (composite.Index, error) {
	return composite.Index{}, nil
}
func (fixedAssembler) LabelEnv(*config.Snapshot, string) map[string]string { return nil }

// cells is the Cells double: a host cell is the project root; a worktree
// cell is a fresh directory per launch. Both root the session dir under one
// sessions root, keyed by harp.
type cells struct{ sessions, worktrees string }

func (c *cells) Prepare(_ context.Context, req launch.CellRequest) (launch.Cell, error) {
	workspace := req.ProjectRoot
	if req.Axes.Workspace == launch.WorkspaceWorktree {
		workspace = filepath.Join(c.worktrees, req.Identity.Harp)
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			return launch.Cell{}, err
		}
	}
	sessionDir := filepath.Join(c.sessions, req.Identity.Harp)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return launch.Cell{}, err
	}
	paths := present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: workspace},
		CtxloomHome: present.Root{Host: req.Host.CtxloomHome},
		Scratch:     present.Root{Host: sessionDir},
	})
	return launch.Cell{Paths: paths, Workspace: workspace, Env: map[string]string{}, Cleanup: func() error { return nil }}, nil
}

// recordingDriver is the Driver double: it records what the runner asked
// it to drive.
type recordingDriver struct{ turns []coord.Turn }

func (d *recordingDriver) Drive(_ context.Context, t coord.Turn) error {
	d.turns = append(d.turns, t)
	return nil
}

// staticWriter is the ONE static writer over the real filesystem, and
// records an empty ownership record for one test.
func staticWriter(t *testing.T) *fsstatic.Static {
	t.Helper()
	return fsstatic.New(afero.NewOsFs())
}

func records(t *testing.T) delivery.Ownership {
	t.Helper()
	rec, err := confpatch.NewRecords(afero.NewOsFs(), filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	return rec
}

// cellTree is every file a delivery left in a launch's cell — under its
// session root and its workspace — keyed by its path relative to that root,
// with the digest of its bytes; what two arms are compared by. The one
// per-session value a delivered file legitimately carries, the session's
// own minted endpoint (URL and bearer), is normalized before digesting.
func cellTree(t *testing.T, l launch.Launch) map[string]string {
	t.Helper()
	out := map[string]string{}
	normalize := strings.NewReplacer(l.MCP.URL, "<endpoint>", l.MCP.Credential, "<bearer>")
	for prefix, root := range map[string]string{"session": l.Cell.Paths.Paths().Scratch.Host, "workspace": l.Cell.Workspace} {
		for rel, digest := range treeOf(t, root, normalize) {
			out[prefix+"/"+rel] = digest
		}
	}
	return out
}

// treeOf is every regular file under root, keyed by its path relative to
// root, with the digest of its normalized bytes.
func treeOf(t *testing.T, root string, normalize *strings.Replacer) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if strings.HasPrefix(rel, ".git") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%x", sha256.Sum256([]byte(normalize.Replace(string(b)))))
		return nil
	})
	require.NoError(t, err)
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestExecute_ABindingsRootSelection_LandsTheKindAtTheSharedRoot: a binding
// that selects the project root for its MCP surface has that file delivered
// into its workspace — the shared root as a SELECTION the plan carries and
// the writer honours — while everything else stays under the session home.
func TestExecute_ABindingsRootSelection_LandsTheKindAtTheSharedRoot(t *testing.T) {
	env := newDeliveryEnv(t)
	shared := agents.Agent{Name: "shared", Profiles: []string{"base"}, Permissions: "bypass", Roots: map[string]string{"mcp": "project-root"}}
	env.deps.Snapshot.Config = config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"primary": {Type: string(backends.NewMock().Name())}},
			Defaults: config.RoleDefaults{Primary: "primary"},
		},
		Agents:       map[string]agents.Agent{"shared": shared},
		DefaultAgent: "shared",
	})
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-shared"),
		Agent:    "shared", Mode: engine.Structured, Prompt: "go", WorkDir: env.project, Workspace: launch.WorkspaceWorktree,
	})
	require.NoError(t, err)
	var mcpRoot present.RootKind
	for _, it := range l.Plan.Static {
		if it.Kind == present.MCP {
			mcpRoot = it.Root
		}
	}
	require.Equal(t, present.RootProjectRoot, mcpRoot, "the plan carries the binding's selection")

	drive := &recordingDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	require.NoError(t, err)
	tree := cellTree(t, l)
	require.Contains(t, tree, "workspace/.mock/mcp.json", "the MCP file landed at the shared root")
	require.Contains(t, tree, "session/MOCK_CONTEXT.md", "the rest stayed under the session home")
	require.NotContains(t, tree, "session/.mock/mcp.json")
}
