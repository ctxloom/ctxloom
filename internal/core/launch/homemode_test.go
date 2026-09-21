package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// TestResolve_EngineHome_SessionByDefault_HostOnlyBySelection: the home mode
// the cell is asked for is the SESSION home unless the binding selects the
// real one by name (ruled 2026-09-21). A binding that says nothing, a
// binding whose spelling does not parse under --degraded, and a launch with
// no binding at all (a profile set) all land on the session home; only an
// explicit `engine_home: host` reaches the host's — the unsafe selection.
func TestResolve_EngineHome_SessionByDefault_HostOnlyBySelection(t *testing.T) {
	cases := []struct {
		name string
		src  func(env launchtest.Env) launch.Source
		want launch.HomeMode
	}{
		{"a binding that declares nothing", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Agent: "silent", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project}
		}, launch.HomeModeSession},
		{"a binding restating session", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Agent: "session", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project}
		}, launch.HomeModeSession},
		{"a binding selecting the host home", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Agent: "host", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project}
		}, launch.HomeModeHost},
		{"an unparseable spelling under --degraded", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Agent: "typo", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project, Degraded: true}
		}, launch.HomeModeSession},
		{"a launch with no binding (a profile set)", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Structured, Prompt: "x", WorkDir: env.Project}
		}, launch.HomeModeSession},
		{"an internal one-shot", func(env launchtest.Env) launch.Source {
			return launch.Source{Identity: env.Identity, Internal: true, Mode: engine.Structured, Prompt: "x", WorkDir: env.Project}
		}, launch.HomeModeSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := launchtest.Deps(t,
				launchtest.WithAgent("silent"),
				launchtest.WithAgent("session", launchtest.EngineHome("session")),
				launchtest.WithAgent("host", launchtest.EngineHome("host")),
				launchtest.WithAgent("typo", launchtest.EngineHome("hostt")),
			)
			l, err := launch.Resolve(context.Background(), env.Deps, tc.src(env))
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, tc.want, env.LastCellRequest().HomeMode, "the cell is asked for the home mode the resolver settled")
			require.Equal(t, tc.want, l.Cell.HomeMode, "the cell carries the home mode it was prepared under, for whoever renders it")
		})
	}
}

// An unparseable spelling without --degraded is refused by name: it is
// neither the default nor a selection.
func TestResolve_EngineHome_UnparseableSpellingIsRefused(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("typo", launchtest.EngineHome("hostt")))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: env.Identity, Agent: "typo", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "hostt")
}
