package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: engine.PermissionPlan, Axes: launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost}}},
		{"profile set", launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: engine.PermissionDefault}},
		{"label override", launch.Source{Identity: env.Identity, Agent: "dev", Label: "fast", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "fast", Permission: engine.PermissionPlan}},
		{"init probe", launch.Source{Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "ping", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: engine.PermissionBypass}},
		{"internal one-shot", launch.Source{Identity: env.Identity, Agent: "distiller", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "payload", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "fast", Permission: engine.PermissionBypass}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := launch.Resolve(context.Background(), env.Deps, tc.src)
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			tc.want.Assert(t, l)
			require.Equal(t, tc.src.Identity, l.Identity, "the identity the caller minted is the one the launch carries")
			require.NotZero(t, l.Permission, "the permission is decided here, not downstream")
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
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "pty-only", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrModeUnsupported) // fail loud where no native surface exists: Modes, read here, not a driver probe

	env = launchtest.Deps(t, launchtest.WithAgent("boxed", launchtest.Runtime(launch.RuntimeRootful)), launchtest.RuntimesAvailable(launch.RuntimeRootless))
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "boxed", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrOwnershipMismatch) // fatal, never a substitution

	env = launchtest.Deps(t, launchtest.WithAgent("imageless", launchtest.Runtime(launch.RuntimeRootless)), launchtest.RuntimesAvailable(launch.RuntimeRootless), launchtest.EngineWithoutContainer())
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "imageless", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
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
			require.Equal(t, engine.PermissionDefault, l.Permission, "the host default stands: never floored to plan, never widened to bypass")
			require.Empty(t, got, "nothing was dropped, so nothing is announced")

			l, err = launch.Resolve(context.Background(), deps, launch.Source{Identity: id, Agent: "careful", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project, Degraded: degraded})
			require.NoError(t, err)
			discardCareful := l
			t.Cleanup(func() { _ = launch.Discard(context.Background(), discardCareful) })
			require.Equal(t, engine.PermissionAcceptEdits, l.Permission, "a declared prompting posture is honoured as declared")
		}
	}
}

// TestResolve_MCPEndpoint_PerSession_StableAcrossResume: the endpoint is
// minted once per harp in Resolve; a resume of the same harp reuses it; only
// an explicit rebind mints a fresh one.
func TestResolve_MCPEndpoint_PerSession_StableAcrossResume(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	env.Deps.Endpoints = &launchtest.StableMinter{}
	first, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	resumed, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: engine.PermissionBypass, WorkDir: env.Project,
		Resume: launch.Resume{Ref: sessions.ResumeRef{Harp: env.Identity.Harp, NativeKey: "k1"}}})
	require.NoError(t, err)
	require.Equal(t, first.MCP, resumed.MCP, "a resumed session keeps its endpoint and credential")
	require.Equal(t, "k1", resumed.Resume.NativeKey)

	entry, err := env.Deps.Sessions.Find(env.Identity.Harp)
	require.NoError(t, err)
	require.Equal(t, first.MCP, entry.MCP, "the endpoint is bound on the session record")
	require.Equal(t, string(first.Engine), entry.Backend, "the engine Resolve decided is recorded on the session")

	rebound, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: engine.PermissionBypass, WorkDir: env.Project,
		Resume: launch.Resume{Ref: sessions.ResumeRef{Harp: env.Identity.Harp}, RebindEndpoint: true}})
	require.NoError(t, err)
	require.NotEqual(t, first.MCP.URL, rebound.MCP.URL, "an explicit rebind mints a fresh address")
}

// TestRebindEndpoint_MintsAndRecordsANewAddressOnly: a held launch whose
// runner could not bind its address gets a fresh one, bound on the session
// record (so a later resume finds what the runner serves), and nothing else
// about the launch moves.
func TestRebindEndpoint_MintsAndRecordsANewAddressOnly(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	env.Deps.Endpoints = &launchtest.StableMinter{}
	first, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)

	rebound, err := launch.RebindEndpoint(context.Background(), env.Deps, first)
	require.NoError(t, err)
	require.NotEqual(t, first.MCP.URL, rebound.MCP.URL, "a rebind mints a fresh address")
	require.NotEqual(t, first.MCP.Credential, rebound.MCP.Credential, "and a fresh credential")

	entry, err := env.Deps.Sessions.Find(env.Identity.Harp)
	require.NoError(t, err)
	require.Equal(t, rebound.MCP, entry.MCP, "the session record names the rebound endpoint, not the lost one")

	// Launch holds func values (the cell's handles), which no deep equality
	// compares; the fields a rebind could plausibly disturb are checked.
	require.Equal(t, first.Identity, rebound.Identity, "the rebind keeps the session")
	require.Equal(t, first.Engine, rebound.Engine)
	require.Equal(t, first.Prompt, rebound.Prompt)
	require.Equal(t, first.Plan, rebound.Plan)
	require.Equal(t, first.Cell.Workspace, rebound.Cell.Workspace)
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
	require.Equal(t, l.Cell.Workspace, s.WorkDir)
	require.Equal(t, l.Cell.Paths.Paths(), s.Roots)
	require.Equal(t, l.MCP, s.MCP)
	require.Equal(t, "hello", s.Prompt)
}

// TestResolve_InternalSource_BindsNoAgent: an internal one-shot names no
// binding and no profiles — its prompt is the whole instruction and the
// label names its engine — yet it is a real session: a harp, an endpoint,
// the managed surfaces, the headless floor.
func TestResolve_InternalSource_BindsNoAgent(t *testing.T) {
	env := launchtest.Deps(t)
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Internal: true, Label: "fast", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "distill this", WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, "fast", l.Label.Label)
	pkg, err := launch.Open(context.Background(), env.Deps, l)
	require.NoError(t, err, "the package still rides, carried: the one-shot is a real session")
	require.Empty(t, pkg.Selection.Profiles, "no binding: no profiles composed")
	require.Empty(t, pkg.Context.Text)
	require.NotNil(t, l.Plan.Static, "a plan exists even when empty")
	require.Equal(t, engine.PermissionBypass, l.Permission)
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

// TestResolve_Permission_TheChain: the flag, the binding, the label, the
// project default, the engine's host default — the first declared wins; an
// enforcing engine keeps a declared plan.
func TestResolve_Permission_TheChain(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.WithAgent("strict", launchtest.Permissions("plan")), launchtest.ProjectPermissions("bypass"))
	at := func(src launch.Source) engine.PermissionMode {
		t.Helper()
		src.Identity, src.Mode, src.WorkDir = env.Identity, engine.Interactive, env.Project
		l, err := launch.Resolve(context.Background(), env.Deps, src)
		require.NoError(t, err)
		return l.Permission
	}
	require.Equal(t, engine.PermissionBypass, at(launch.Source{Agent: "dev"}), "the project default fills an undeclared binding")
	require.Equal(t, engine.PermissionPlan, at(launch.Source{Agent: "strict"}), "the binding beats the project default")
	require.Equal(t, engine.PermissionPlan, at(launch.Source{Agent: "dev", Label: "guarded"}), "the label beats the project default")
	require.Equal(t, engine.PermissionAcceptEdits, at(launch.Source{Agent: "strict", Permission: engine.PermissionAcceptEdits}), "the flag beats everything")

	env = launchtest.Deps(t, launchtest.WithAgent("dev"))
	require.Equal(t, engine.PermissionDefault, at(launch.Source{Agent: "dev"}), "nothing declared anywhere: the engine's host default")
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

// TestResolve_Permission_PlanCollapsesOnEveryPath: on an engine with no
// read-only tier a declared plan is not enforced and collapses to default —
// on an interactive run, and on every structured run alike. One floor, no
// path skips the collapse.
func TestResolve_Permission_PlanCollapsesOnEveryPath(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("planner", launchtest.Permissions("plan")), launchtest.NoReadOnlyPlan())
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "planner", Mode: engine.Interactive, WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, engine.PermissionDefault, l.Permission, "interactive: plan collapses to default, which prompts")

	child := env.Identity
	child.Depth = 1
	for _, id := range []sessions.Identity{env.Identity, child} {
		l, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "planner", Mode: engine.Structured, WorkDir: env.Project})
		require.NoError(t, err)
		discard := l
		t.Cleanup(func() { _ = launch.Discard(context.Background(), discard) })
		require.Equal(t, engine.PermissionDefault, l.Permission, "depth %d: structured collapses the same way, never launched with a flag the engine ignores", id.Depth)
	}
}

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
	small, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	require.Nil(t, small.Package.Claim, "under the ceiling the bytes ride the frame")

	env.Deps.InlineMax = -1 // every package is above the ceiling
	large, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	require.NotNil(t, large.Package.Claim, "above the ceiling a claim rides the frame")

	enc, err := composite.Encode(env.Assembled())
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
		{"project config", launchtest.Deps(t, launchtest.WithAgent("dev"), launchtest.ProjectPermissions("yolo")), "dev", "project config", "yolo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got report.Findings
			deps := tc.env.Deps
			deps.Reporter = &got
			l, err := launch.Resolve(context.Background(), deps, launch.Source{Identity: tc.env.Identity, Agent: tc.agent, Mode: engine.Interactive, Prompt: "x", WorkDir: tc.env.Project, Degraded: true})
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, engine.PermissionPlan, l.Permission)
			require.Len(t, got, 1, "the drop to the floor is announced, never silent")
			require.Contains(t, got[0].Text, `"`+tc.bad+`"`)
			require.Contains(t, got[0].Text, tc.from)
			require.Contains(t, got[0].Text, "default|acceptEdits|plan|bypass")
		})
	}
}
