package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// The cell is asked for the binding's declared auth mode, and for the token
// when nothing declares one: a binding that says nothing, an unparseable
// spelling under --degraded, a profile-set launch and an internal one-shot
// all get the token; only a binding naming login reaches the human's own.
func TestResolve_Auth_TokenByDefault_OtherModesByDeclaration(t *testing.T) {
	src := func(env launchtest.Env, agent string, degraded bool) launch.Source {
		return launch.Source{Identity: env.Identity, Agent: agent, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project, Degraded: degraded}
	}
	cases := []struct {
		name string
		src  func(env launchtest.Env) launch.Source
		want engine.AuthMode
	}{
		{"a binding that declares nothing", func(env launchtest.Env) launch.Source { return src(env, "silent", false) }, engine.AuthToken},
		{"a binding declaring login", func(env launchtest.Env) launch.Source { return src(env, "login", false) }, engine.AuthLogin},
		{"a binding declaring api-key", func(env launchtest.Env) launch.Source { return src(env, "key", false) }, engine.AuthAPIKey},
		{"an unparseable spelling under --degraded", func(env launchtest.Env) launch.Source { return src(env, "typo", true) }, engine.AuthToken},
		{"a launch with no binding (a profile set)", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project}
		}, engine.AuthToken},
		{"an internal one-shot", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Internal: true, Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project}
		}, engine.AuthToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := launchtest.Deps(t,
				launchtest.WithAgent("silent"),
				launchtest.WithAgent("login", launchtest.Auth("login")),
				launchtest.WithAgent("key", launchtest.Auth("api-key")),
				launchtest.WithAgent("typo", launchtest.Auth("apikey")),
			)
			l, err := launch.Resolve(context.Background(), env.Deps, tc.src(env))
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, tc.want, env.LastCellRequest().Auth)
		})
	}
}

// An unparseable auth without --degraded is refused by name, never defaulted.
func TestResolve_Auth_UnparseableSpellingIsRefused(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("typo", launchtest.Auth("apikey")))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: env.Identity, Agent: "typo", Mode: engine.Structured, Permission: engine.PermissionBypass, Prompt: "x", WorkDir: env.Project,
	})
	require.ErrorContains(t, err, "apikey")
}
