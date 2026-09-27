package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// The cell is asked for the auth mode the binding DECLARED, verbatim —
// undeclared stays undeclared, and even a spelling that will not parse
// travels as written — because the one check (engine.CheckAuth) runs in the
// cells adapter against the engine the launch binds. An internal launch
// carries the mode its caller names (the setup probe: the default agent's).
func TestResolve_Auth_TheDeclaredModeReachesTheCellVerbatim(t *testing.T) {
	src := func(env launchtest.Env, agent string) launch.Source {
		return launch.Source{Identity: env.Identity, Agent: agent, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project}
	}
	cases := []struct {
		name string
		src  func(env launchtest.Env) launch.Source
		want engine.AuthMode
	}{
		{"a binding that declares nothing", func(env launchtest.Env) launch.Source { return src(env, "silent") }, ""},
		{"a binding declaring login", func(env launchtest.Env) launch.Source { return src(env, "login") }, engine.AuthLogin},
		{"a binding declaring cloud", func(env launchtest.Env) launch.Source { return src(env, "cloud") }, engine.AuthCloud},
		{"an unparseable spelling", func(env launchtest.Env) launch.Source { return src(env, "typo") }, "apikey"},
		{"a launch with no binding (a profile set)", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project}
		}, ""},
		{"an internal one-shot naming a mode", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Internal: true, Auth: engine.AuthLogin, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project}
		}, engine.AuthLogin},
		{"an internal one-shot naming none", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Internal: true, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := launchtest.Deps(t,
				launchtest.WithAgent("silent"),
				launchtest.WithAgent("login", launchtest.Auth("login")),
				launchtest.WithAgent("cloud", launchtest.Auth("cloud")),
				launchtest.WithAgent("typo", launchtest.Auth("apikey")),
			)
			l, err := launch.Resolve(context.Background(), env.Deps, tc.src(env))
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, tc.want, env.LastCellRequest().Auth)
		})
	}
}
