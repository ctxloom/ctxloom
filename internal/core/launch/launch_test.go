package launch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolve_EverySource_OneResolver: every way a launch is asked for goes
// through ONE resolver; the caller supplies the identity it minted and gets
// it back unchanged (Resolve never mints).
func TestResolve_EverySource_OneResolver(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev", launchtest.Runtime(launch.RuntimeHost), launchtest.Permissions("plan")))
	cases := []struct {
		name string
		src  launch.Source
		want launchtest.Expect
	}{
		{"agent binding", launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: "plan", Axes: launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost}}},
		{"profile set", launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: "default"}},
		{"label override", launch.Source{Identity: env.Identity, Agent: "dev", Label: "fast", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "fast", Permission: "plan"}},
		{"init probe", launch.Source{Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Permission: "bypass", Prompt: "ping", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: "bypass"}},
		{"internal one-shot", launch.Source{Identity: env.Identity, Agent: "distiller", Mode: engine.Structured, Permission: "bypass", Prompt: "payload", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "fast", Permission: "bypass"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := launch.Resolve(context.Background(), env.Deps, tc.src)
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			tc.want.Assert(t, l)
			require.Equal(t, tc.src.Identity, l.Identity, "the identity the caller minted is the one the launch carries")
			require.NotEmpty(t, launchtest.ModeOf(l), "the permission is decided here, not downstream")
			require.NotEmpty(t, l.MCP.URL, "the session endpoint is minted here, not by the runner")
			require.NotNil(t, l.Plan.Static, "a plan exists even when empty")
		})
	}
}

// TestResolve_Refuses_TheIncompleteShapes: the constructor is the gate.
func TestResolve_Refuses_TheIncompleteShapes(t *testing.T) {
	env := launchtest.Deps(t)
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Agent: "dev", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrNoIdentity) // the caller mints; a zero identity is refused

	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "nobody", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrNoAgent) // an agent that does not resolve is refused by name
	require.ErrorContains(t, err, "nobody")

	env = launchtest.Deps(t, launchtest.WithAgent("pty-only", launchtest.NoStructuredDrive()))
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "pty-only", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrModeUnsupported) // fail loud where no native surface exists: Modes, read here, not a driver probe

	env = launchtest.Deps(t, launchtest.WithAgent("boxed", launchtest.Runtime(launch.RuntimeRootful)), launchtest.RuntimesAvailable(launch.RuntimeRootless))
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "boxed", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrOwnershipMismatch) // fatal, never a substitution

	env = launchtest.Deps(t, launchtest.WithAgent("imageless", launchtest.Runtime(launch.RuntimeRootless)), launchtest.RuntimesAvailable(launch.RuntimeRootless), launchtest.EngineWithoutContainer())
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "imageless", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	var unsupported engine.ErrUnsupported
	require.ErrorAs(t, err, &unsupported) // the engine's own Container() refused; Resolve passes it through untouched
	require.Equal(t, "container", unsupported.Capability)
}

// TestResolve_Permission_FlooredOnce: the floor is applied here and nowhere
// else. A posture the engine cannot honour is refused ONCE, at the floor.
func TestResolve_Permission_FlooredOnce(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("typo", launchtest.Permissions("plann")))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "typo", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
	require.ErrorContains(t, err, "plann")
}

// TestResolve_Permission_HeadlessTakesItsPosture: a Structured run has no
// human at the engine, and that is no longer a reason to refuse or reshape
// its posture — the engine denies what nothing resolves (claude's
// --permission-prompts none) and the denial is surfaced, so the run launches
// at exactly the posture it declared, or the engine's host default. The
// originator's own run (depth 0) and a delegated child (depth > 0) follow
// ONE rule, and --degraded changes nothing about a posture that resolved.
func TestResolve_Permission_HeadlessTakesItsPosture(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("silent"), launchtest.WithAgent("careful", launchtest.Permissions("acceptEdits")))
	child := env.Identity
	child.Depth = 1
	for _, id := range []sessions.Identity{env.Identity, child} {
		for _, degraded := range []bool{false, true} {
			var got report.Findings
			deps := env.Deps
			deps.Reporter = &got
			l, err := launch.Resolve(context.Background(), deps, launch.Source{Identity: id, Agent: "silent", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project, Degraded: degraded})
			require.NoError(t, err, "depth %d degraded %v: a headless run is never refused for its posture", id.Depth, degraded)
			discard := l
			t.Cleanup(func() { _ = launch.Discard(context.Background(), discard) })
			require.Equal(t, "default", launchtest.ModeOf(l), "the engine default stands: never floored to plan, never widened to bypass")
			require.Empty(t, got, "nothing was dropped, so nothing is announced")

			l, err = launch.Resolve(context.Background(), deps, launch.Source{Identity: id, Agent: "careful", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project, Degraded: degraded})
			require.NoError(t, err)
			discardCareful := l
			t.Cleanup(func() { _ = launch.Discard(context.Background(), discardCareful) })
			require.Equal(t, "acceptEdits", launchtest.ModeOf(l), "a declared prompting posture is honoured as declared")
		}
	}
}

// TestResolve_MCPEndpoint_FreshPerLaunch: every launch mints its own
// endpoint, a resume of the same harp included, and nothing about it is
// persisted. A bearer that outlived its launch must not authenticate the next
// one, and an orphan of incarnation N must not reach incarnation N+1. The
// store is the on-disk Manager so the sidecar's bytes can be read.
func TestResolve_MCPEndpoint_FreshPerLaunch(t *testing.T) {
	testsupport.Isolate(t)
	store, err := sessions.Open(nil)
	require.NoError(t, err)
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	entry, err := store.AssignHarp(env.Project, "")
	require.NoError(t, err)
	env.Deps.Sessions = store
	id := sessions.Identity{Harp: entry.HarpName, Project: env.Identity.Project}

	first, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "dev", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	require.NoError(t, store.BindSession(id.Harp, "native-1", ""))
	resumed, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "dev", Mode: engine.Structured, Permission: "bypass", WorkDir: env.Project,
		Resume: launch.Resume{Ref: sessions.ResumeRef{Harp: id.Harp}}})
	require.NoError(t, err)
	require.NotEqual(t, first.MCP.URL, resumed.MCP.URL, "a resume binds a fresh address")
	require.NotEqual(t, first.MCP.Credential, resumed.MCP.Credential, "and presents a fresh credential")
	require.Equal(t, "native-1", resumed.Resume.NativeKey, "the resume still continues the native session the record names")

	got, err := store.Find(id.Harp)
	require.NoError(t, err)
	require.Equal(t, string(first.Engine), got.Backend, "the engine Resolve decided is still recorded on the session")
	raw, err := os.ReadFile(filepath.Join(store.Root(), id.Harp, paths.SessionSidecarFileName))
	require.NoError(t, err)
	for _, l := range []launch.Launch{first, resumed} {
		require.NotContains(t, string(raw), l.MCP.Credential, "no bearer is written at rest")
		require.NotContains(t, string(raw), l.MCP.URL, "nor the address")
	}
}

// TestRebindEndpoint_MintsANewAddressOnly: a held launch whose runner could
// not bind its address gets a fresh one, and nothing else about the launch
// moves.
func TestRebindEndpoint_MintsANewAddressOnly(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	env.Deps.Endpoints = &launchtest.StableMinter{}
	first, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)

	rebound, err := launch.RebindEndpoint(context.Background(), env.Deps, first)
	require.NoError(t, err)
	require.NotEqual(t, first.MCP.URL, rebound.MCP.URL, "a rebind mints a fresh address")
	require.NotEqual(t, first.MCP.Credential, rebound.MCP.Credential, "and a fresh credential")

	// Launch holds func values (the cell's handles), which no deep equality
	// compares; the fields a rebind could plausibly disturb are checked.
	require.Equal(t, first.Identity, rebound.Identity, "the rebind keeps the session")
	require.Equal(t, first.Engine, rebound.Engine)
	require.Equal(t, first.Prompt, rebound.Prompt)
	require.Equal(t, first.Plan, rebound.Plan)
	require.Equal(t, first.Cell.Paths, rebound.Cell.Paths)
}

// TestLaunch_WorkDirReadsTheProjectRootSideItsReaderSees: a relocated project
// root has two sides. The engine is started in the side IT sees; the delivery
// writer works in the side the writing process opens (Mapped.Host — which the
// runner has already rewritten to the engine side before it delivers).
func TestLaunch_WorkDirReadsTheProjectRootSideItsReaderSees(t *testing.T) {
	l := launch.Launch{Cell: launch.Cell{Placement: launch.Placement{Paths: present.Advised(present.Paths{
		ProjectRoot: present.Root{Host: "/host/proj", Engine: "/work"},
	})}}}
	assert.Equal(t, "/work", l.Session().WorkDir, "the engine's cwd is the project as the engine sees it")
	assert.Equal(t, "/host/proj", l.Loadout(composite.Package{}).WorkDir, "delivery writes where the writing process opens the project")
}

// TestLaunch_Session_IsTheOnlyProjection: Session() carries what the engine
// is fed and nothing the runner keeps.
func TestLaunch_Session_IsTheOnlyProjection(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Interactive, Prompt: "hello", WorkDir: env.Project})
	require.NoError(t, err)
	s := l.Session()
	require.Equal(t, l.Identity, s.Identity)
	require.Equal(t, l.Label, s.Label)
	require.Equal(t, l.Permission, s.Permission)
	require.Equal(t, l.Cell.Paths.Paths().ProjectRoot.Engine, s.WorkDir)
	require.Equal(t, l.Cell.Paths.Paths(), s.Roots)
	require.Equal(t, l.MCP, s.MCP)
	require.Equal(t, "hello", s.Prompt)
	require.Equal(t, engine.TrustUntrusted, s.Trust, "no verdict from the cell is no trust")
}

// TestResolve_TheCellsVerdictReachesTheSession: the cells adapter takes the
// engine's verdict on the repository where it prepared the cell; the launch
// carries it (across the wire to the runner) and the engine's session is
// bound with it, which is what decides the repository-source flags.
func TestResolve_TheCellsVerdictReachesTheSession(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.WithRepoTrust(engine.TrustTrusted))
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Interactive, Prompt: "hello", WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, engine.TrustTrusted, l.Trust)
	require.Equal(t, engine.TrustTrusted, l.Session().Trust)
}

// TestResolve_InternalSource_BindsNoAgent: an internal one-shot names no
// binding and no profiles — its prompt is the whole instruction and the
// label names its engine — yet it is a real session: a harp, an endpoint,
// the managed surfaces, the headless floor.
func TestResolve_InternalSource_BindsNoAgent(t *testing.T) {
	env := launchtest.Deps(t)
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Internal: true, Label: "fast", Mode: engine.Structured, Permission: "bypass", Prompt: "distill this", WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, "fast", l.Label.Label)
	pkg, err := launch.Open(context.Background(), env.Deps, l)
	require.NoError(t, err, "the package still rides, carried: the one-shot is a real session")
	require.Empty(t, pkg.Selection.Profiles, "no binding: no profiles composed")
	require.Empty(t, pkg.Context.Text)
	require.NotNil(t, l.Plan.Static, "a plan exists even when empty")
	require.Equal(t, "bypass", launchtest.ModeOf(l))
	require.NotEmpty(t, l.MCP.URL)
}

// TestResolve_Label_Precedence: the label override beats what the profiles
// declared, which beats the project's primary; a configured label maps to
// its engine and model (an alias the engine declares is applied); a bare
// registered engine name is admitted as the ad-hoc form; a label that names
// nothing is refused by name.
func TestResolve_Label_Precedence(t *testing.T) {
	src := func(env launchtest.Env, label string) launch.Source {
		return launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Label: label, Mode: engine.Interactive, WorkDir: env.Project}
	}
	env := launchtest.Deps(t, launchtest.ProfileLLM("fast"))
	l, err := launch.Resolve(context.Background(), env.Deps, src(env, ""))
	require.NoError(t, err)
	require.Equal(t, "fast", l.Label.Label, "the profiles' declared label beats the primary")
	require.Equal(t, "fixture-fast-2", l.Label.Model, "the engine's declared alias is applied to the label's model")

	l, err = launch.Resolve(context.Background(), env.Deps, src(env, "primary"))
	require.NoError(t, err)
	require.Equal(t, "primary", l.Label.Label, "the override beats the profiles' declaration")

	env = launchtest.Deps(t)
	l, err = launch.Resolve(context.Background(), env.Deps, src(env, ""))
	require.NoError(t, err)
	require.Equal(t, "primary", l.Label.Label, "nothing declared: the project's primary")

	l, err = launch.Resolve(context.Background(), env.Deps, src(env, string(launchtest.EngineName)))
	require.NoError(t, err)
	require.Equal(t, launchtest.EngineName, l.Engine, "a bare registered engine name is the ad-hoc form")

	_, err = launch.Resolve(context.Background(), env.Deps, src(env, "no-such-label"))
	require.ErrorIs(t, err, launch.ErrNoEngine)
	require.ErrorContains(t, err, "no-such-label")

	l, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Label: "fast", Model: "override-model", Mode: engine.Interactive, WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, "override-model", l.Label.Model, "the caller's model override beats the label's")
}

// TestResolve_Permission_TheChain: the flag, the binding's block for the
// engine, the label's keys, the engine's default — the first declared
// wins. The project declares no mode: it knows no engine.
func TestResolve_Permission_TheChain(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.WithAgent("strict", launchtest.Permissions("bypass")))
	at := func(src launch.Source) string {
		t.Helper()
		src.Identity, src.Mode, src.WorkDir = env.Identity, engine.Interactive, env.Project
		l, err := launch.Resolve(context.Background(), env.Deps, src)
		require.NoError(t, err)
		return launchtest.ModeOf(l)
	}
	require.Equal(t, "default", at(launch.Source{Agent: "dev"}), "nothing declared: the engine's default")
	require.Equal(t, "plan", at(launch.Source{Agent: "dev", Label: "guarded"}), "the label's keys fill an undeclared binding")
	require.Equal(t, "bypass", at(launch.Source{Agent: "strict", Label: "guarded"}), "the binding beats the label")
	require.Equal(t, "acceptEdits", at(launch.Source{Agent: "strict", Permission: "acceptEdits"}), "the flag beats everything")
}

// TestResolve_Axes_ProjectRuntimeTypoIsRefused: the project's `runtime:`
// is parsed once, and a spelling the vocabulary does not admit refuses the
// launch rather than reading as the host.
func TestResolve_Axes_ProjectRuntimeTypoIsRefused(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.ProjectRuntime("contianer-rootless"))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Interactive, WorkDir: env.Project})
	require.Error(t, err)
	require.ErrorContains(t, err, "contianer-rootless")
	require.ErrorContains(t, err, "host|container-rootless|container-rootful")

	env = launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.ProjectRuntime(string(launch.RuntimeHost)))
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Interactive, WorkDir: env.Project, Workspace: launch.WorkspaceWorktree})
	require.NoError(t, err)
	require.Equal(t, launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost}, l.Axes, "the invocation's workspace and the project's runtime")
}

// TestResolve_Axes_DeclaredIsKeptApartFromSettled: the launch carries the
// axes as they were ASKED beside the axes it settled on. An axis nothing
// declared stays empty on Declared while Axes carries its default, so a
// reader of the launch (the --dry-run preview) can say what was asked
// without showing a default as a guarantee somebody declared.
func TestResolve_Axes_DeclaredIsKeptApartFromSettled(t *testing.T) {
	env := launchtest.Deps(t,
		launchtest.WithAgent("dev"),
		launchtest.WithAgent("boxed", launchtest.Runtime(launch.RuntimeRootless)),
		launchtest.RuntimesAvailable(launch.RuntimeRootless))
	at := func(src launch.Source) launch.Launch {
		t.Helper()
		src.Identity, src.Mode, src.WorkDir = env.Identity, engine.Interactive, env.Project
		l, err := launch.Resolve(context.Background(), env.Deps, src)
		require.NoError(t, err)
		t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
		return l
	}

	l := at(launch.Source{Agent: "dev"})
	require.Equal(t, launch.Axes{}, l.Declared, "nothing declared either axis")
	require.Equal(t, launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost}, l.Axes, "both settled to their defaults")

	l = at(launch.Source{Agent: "boxed", Workspace: launch.WorkspaceWorktree})
	require.Equal(t, launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeRootless}, l.Declared, "the invocation's workspace and the binding's runtime, as declared")
	require.Equal(t, l.Declared, l.Axes, "a fully declared request settles to itself")

	env = launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.ProjectRuntime(string(launch.RuntimeHost)))
	l = at(launch.Source{Agent: "dev"})
	require.Equal(t, launch.Axes{}, l.Declared, "the project default is not a declaration: it fills Axes, never Declared")
	require.Equal(t, launch.RuntimeHost, l.Axes.Runtime)
}

// TestResolve_NamedProfilesThatAssembleToNothingAreRefused: naming a
// specialisation and delivering none of it is a failed assembly.
func TestResolve_NamedProfilesThatAssembleToNothingAreRefused(t *testing.T) {
	env := launchtest.Deps(t)
	env.Deps.Assembler = emptyAssembler{}
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Interactive, WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrContextEmpty)
}

// emptyAssembler composes every profile set to nothing.
type emptyAssembler struct{}

func (emptyAssembler) Assemble(context.Context, *config.Snapshot, launch.Selection) (composite.Package, error) {
	return composite.Package{}, nil
}
func (emptyAssembler) Index(context.Context, *config.Snapshot) (composite.Index, error) {
	return composite.Index{}, nil
}
func (emptyAssembler) LabelEnv(*config.Snapshot, string) map[string]string { return nil }

// TestResolve_Carrier_ChosenBySize_RedeemsToTheSamePackage: Resolve measures
// the encoded package and carries it inline under the ceiling, by claim check
// above it; the consumer redeems with the adapter the carrier's shape names
// and decodes the same package either way; a claim whose bytes do not hash
// to the digest is refused.
func TestResolve_Carrier_ChosenBySize_RedeemsToTheSamePackage(t *testing.T) {
	store := memStore{}
	inline, claim := composite.Inline{Max: 1 << 20}, composite.ClaimCheck{Store: store}

	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	env.Deps.Inline, env.Deps.ClaimCheck = inline, claim
	env.Deps.InlineMax = 1 << 20
	small, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	require.Nil(t, small.Package.Claim, "under the ceiling the bytes ride the frame")

	env.Deps.InlineMax = -1 // every package is above the ceiling
	large, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	require.NotNil(t, large.Package.Claim, "above the ceiling a claim rides the frame")

	// What Resolve carries is the assembled package plus what the launch
	// itself adds: this run's approver is the human, so the approval hooks.
	carried := env.Assembled()
	carried.Hooks.Unified = wire.UnifiedHooks{}
	carried.Hooks.Unified.Append(env.Assembled().Hooks.Unified)
	eng, ok := env.Deps.Engines.Lookup(small.Engine)
	require.True(t, ok)
	codec, ok := eng.Approvals().Get()
	require.True(t, ok)
	carried.Hooks.Unified.Append(codec.Hooks(small.Permission.ApprovalTimeout))
	enc, err := composite.Encode(carried)
	require.NoError(t, err)
	for _, c := range []composite.Carrier{small.Package, large.Package} {
		back, err := launchtest.Redeem(context.Background(), inline, claim, c) // the consumer's shape conditional, mirrored from Resolve's size conditional
		require.NoError(t, err)
		require.Equal(t, enc.Digest, back.Digest)
		_, err = composite.Decode(back)
		require.NoError(t, err)
	}
	store[large.Package.Claim.Location] = []byte("tampered")
	back, err := claim.Redeem(context.Background(), large.Package)
	require.NoError(t, err)
	_, err = composite.Decode(back)
	require.ErrorIs(t, err, composite.ErrDigestMismatch)
}

type memStore map[string][]byte

func (m memStore) Put(ctx context.Context, digest [32]byte, b []byte) (string, error) {
	m[string(digest[:])] = b
	return string(digest[:]), nil
}
func (m memStore) Get(ctx context.Context, loc string) ([]byte, error) { return m[loc], nil }

// TestResolve_Permission_DegradedUnparseableIsAnnounced: a declaration that
// does not parse still drops to the floor under --degraded, but says so —
// the bad value, the rung it came from, and the known postures.
func TestResolve_Permission_DegradedUnparseableIsAnnounced(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   launchtest.Env
		agent string
		from  string
		bad   string
	}{
		{"agent binding", launchtest.Deps(t, launchtest.WithAgent("typo", launchtest.Permissions("plann"))), "typo", `agent "typo"`, "plann"},
		{"the flag", launchtest.Deps(t, launchtest.WithAgent("dev")), "dev", "--permissions flag", "yolo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got report.Findings
			deps := tc.env.Deps
			deps.Reporter = &got
			src := launch.Source{Identity: tc.env.Identity, Agent: tc.agent, Mode: engine.Interactive, Prompt: "x", WorkDir: tc.env.Project, Degraded: true}
			if tc.agent == "dev" {
				src.Permission = tc.bad
			}
			l, err := launch.Resolve(context.Background(), deps, src)
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, "plan", launchtest.ModeOf(l))
			require.Len(t, got, 1, "the drop to the floor is announced, never silent")
			require.Contains(t, got[0].Text, `"`+tc.bad+`"`)
			require.Contains(t, got[0].Text, tc.from)
			require.Contains(t, got[0].Text, "default|acceptEdits|plan|bypass")
		})
	}
}

// modeAssembler records the Selection it was asked to assemble.
type modeAssembler struct {
	emptyAssembler
	got []launch.Selection
}

func (a *modeAssembler) Assemble(_ context.Context, _ *config.Snapshot, sel launch.Selection) (composite.Package, error) {
	a.got = append(a.got, sel)
	return composite.Package{Context: composite.Context{Text: "ctx"}}, nil
}

// TestResolve_TheSelectionCarriesWhoReadsTheMail: ctxloom's own turn-start
// mail reader is assembled only for the session OWNER — a human's session
// driven interactively. Every other run is handed its mail as turns by its
// runner, so it must not also carry the reader (rows worried-chief F4,
// tacky-carload): a structured run, a one-turn run at depth 0, and an
// INTERACTIVE delegated child alike. The decision reaches the assembler on
// the Selection.
// MUTATION — decide Selection.Mail from the mode alone — turns the
// interactive child's case red.
func TestResolve_TheSelectionCarriesWhoReadsTheMail(t *testing.T) {
	env := launchtest.Deps(t)
	asm := &modeAssembler{}
	env.Deps.Assembler = asm
	child := env.Identity
	child.Depth = 1
	cases := []struct {
		name string
		id   sessions.Identity
		mode engine.Mode
		want sessions.MailReader
	}{
		{"the owner", env.Identity, engine.Interactive, sessions.MailByHook},
		{"a structured run at depth 0", env.Identity, engine.Structured, sessions.MailByRunner},
		{"an interactive delegated child", child, engine.Interactive, sessions.MailByRunner},
		{"a structured delegated child", child, engine.Structured, sessions.MailByRunner},
	}
	for _, c := range cases {
		_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: c.id, Profiles: []string{"base"}, Mode: c.mode, Permission: "bypass", WorkDir: env.Project})
		require.NoError(t, err, c.name)
	}
	require.Len(t, asm.got, len(cases))
	for i, c := range cases {
		assert.Equal(t, c.want, asm.got[i].Mail, c.name)
	}
}
