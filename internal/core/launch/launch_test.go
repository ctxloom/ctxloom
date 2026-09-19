package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

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
			launchtest.Expect{Engine: "mock", Label: "primary", Permission: engine.PermissionPlan, Axes: launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost}}},
		{"profile set", launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: "mock", Label: "primary", Permission: engine.PermissionDefault}},
		{"label override", launch.Source{Identity: env.Identity, Agent: "dev", Label: "fast", Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project},
			launchtest.Expect{Engine: "mock", Label: "fast", Permission: engine.PermissionPlan}},
		{"init probe", launch.Source{Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Prompt: "ping", WorkDir: env.Project},
			launchtest.Expect{Engine: "mock", Label: "primary", Permission: engine.PermissionBypass}}, // headless floor applied ONCE, here
		{"internal one-shot", launch.Source{Identity: env.Identity, Agent: "distiller", Mode: engine.Structured, Prompt: "payload", WorkDir: env.Project},
			launchtest.Expect{Engine: "mock", Label: "fast", Permission: engine.PermissionBypass}},
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
