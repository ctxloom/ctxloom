package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
		{"init probe", launch.Source{Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Prompt: "ping", WorkDir: env.Project},
			launchtest.Expect{Engine: launchtest.EngineName, Label: "primary", Permission: engine.PermissionBypass}}, // headless floor applied ONCE, here
		{"internal one-shot", launch.Source{Identity: env.Identity, Agent: "distiller", Mode: engine.Structured, Prompt: "payload", WorkDir: env.Project},
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
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "pty-only", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrModeUnsupported) // fail loud where no native surface exists: Modes, read here, not a driver probe

	env = launchtest.Deps(t, launchtest.WithAgent("boxed", launchtest.Runtime(launch.RuntimeRootful)), launchtest.RuntimesAvailable(launch.RuntimeRootless))
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "boxed", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrOwnershipMismatch) // fatal, never a substitution

	env = launchtest.Deps(t, launchtest.WithAgent("imageless", launchtest.Runtime(launch.RuntimeRootless)), launchtest.RuntimesAvailable(launch.RuntimeRootless), launchtest.EngineWithoutContainer())
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "imageless", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project})
	var unsupported engine.ErrUnsupported
	require.ErrorAs(t, err, &unsupported) // the engine's own Container() refused; Resolve passes it through untouched
	require.Equal(t, "container", unsupported.Capability)
}

// TestResolve_Permission_FlooredOnce: the floor is applied here and nowhere
// else. A posture the engine cannot honour is refused ONCE, at the floor; a
// delegated child (depth > 0) that would block on a prompt is refused rather
// than widened, because no human answers a child's engine.
func TestResolve_Permission_FlooredOnce(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("typo", launchtest.Permissions("plann")))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "typo", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured)
	require.ErrorContains(t, err, "plann")

	env = launchtest.Deps(t, launchtest.WithAgent("silent"))
	child := env.Identity
	child.Depth = 1
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: child, Agent: "silent", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured, "a child declaring no headless-safe posture is refused, never widened to bypass")

	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: child, Agent: "silent", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project, Degraded: true})
	require.NoError(t, err)
	require.Equal(t, engine.PermissionPlan, l.Permission, "degraded narrows a child to the most restrictive headless-safe posture")
}

// TestResolve_MCPEndpoint_PerSession_StableAcrossResume: the endpoint is
// minted once per harp in Resolve; a resume of the same harp reuses it; only
// an explicit rebind mints a fresh one.
func TestResolve_MCPEndpoint_PerSession_StableAcrossResume(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("dev"))
	env.Deps.Endpoints = &launchtest.StableMinter{}
	first, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	resumed, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, WorkDir: env.Project,
		Resume: launch.Resume{Ref: sessions.ResumeRef{Harp: env.Identity.Harp, NativeKey: "k1"}}})
	require.NoError(t, err)
	require.Equal(t, first.MCP, resumed.MCP, "a resumed session keeps its endpoint and credential")
	require.Equal(t, "k1", resumed.Resume.NativeKey)

	entry, err := env.Deps.Sessions.Find(env.Identity.Harp)
	require.NoError(t, err)
	require.Equal(t, first.MCP, entry.MCP, "the endpoint is bound on the session record")
	require.Equal(t, string(first.Engine), entry.Backend, "the engine Resolve decided is recorded on the session")

	rebound, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "dev", Mode: engine.Structured, WorkDir: env.Project,
		Resume: launch.Resume{Ref: sessions.ResumeRef{Harp: env.Identity.Harp}, RebindEndpoint: true}})
	require.NoError(t, err)
	require.NotEqual(t, first.MCP.URL, rebound.MCP.URL, "an explicit rebind mints a fresh address")
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
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Internal: true, Label: "fast", Mode: engine.Structured, Prompt: "distill this", WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, "fast", l.Label.Label)
	require.Empty(t, l.Package.Profiles, "no binding: no profiles composed")
	require.Empty(t, l.Package.Context)
	require.NotNil(t, l.Package.Managed, "the managed surfaces still ride: the one-shot is a real session")
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

func (emptyAssembler) Assemble(context.Context, *config.Snapshot, launch.Selection) (launch.Assembled, error) {
	return launch.Assembled{}, nil
}
func (emptyAssembler) LabelEnv(*config.Snapshot, string) map[string]string { return nil }
func (emptyAssembler) Surfaces(context.Context, *config.Snapshot, engine.Name, string, []string, map[string]string) (launch.Surfaces, error) {
	return nil, nil
}

// TestResolve_Permission_PlanCollapsesOnEveryPath: on an engine with no
// read-only tier a declared plan is not enforced and collapses to default —
// on the originator's interactive run, on its structured run (then floored
// to bypass at depth 0) and on a delegated child, which is REFUSED rather
// than launched at a posture it cannot honour. One floor, no path skips
// the collapse.
func TestResolve_Permission_PlanCollapsesOnEveryPath(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("planner", launchtest.Permissions("plan")), launchtest.NoReadOnlyPlan())
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "planner", Mode: engine.Interactive, WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, engine.PermissionDefault, l.Permission, "interactive: plan collapses to default, which prompts")

	l, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "planner", Mode: engine.Structured, WorkDir: env.Project})
	require.NoError(t, err)
	require.Equal(t, engine.PermissionBypass, l.Permission, "the originator's structured run: collapsed, then floored up at depth 0")

	child := env.Identity
	child.Depth = 1
	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: child, Agent: "planner", Mode: engine.Structured, WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrPermissionUnhonoured, "a delegated child declaring an unenforceable plan is refused, never launched with a flag the engine ignores")
}
