package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
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

// TestResolve_Orchestrator_RootIsItsOwn_AnAgentNamesIts: the root session
// (depth 0) is its own orchestrator — the cell is asked for none — and a
// delegated child (depth > 0) carries the orchestrator it was spawned
// under; a child with none is refused, because an agent with no
// orchestrator has no credential to project.
func TestResolve_Orchestrator_RootIsItsOwn_AnAgentNamesIts(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("child", launchtest.Permissions("bypass")))
	root, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = launch.Discard(context.Background(), root) })
	require.Empty(t, env.LastCellRequest().Orchestrator, "the root is its own orchestrator: the cell seeds it whole from the host")

	child := env.Identity
	child.Depth = 1
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: child, Agent: "child", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project, Orchestrator: "root-harp",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
	require.Equal(t, "root-harp", env.LastCellRequest().Orchestrator, "an agent's cell projects the orchestrator's credential")

	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: child, Agent: "child", Mode: engine.Structured, Prompt: "x", WorkDir: env.Project,
	})
	require.ErrorIs(t, err, launch.ErrNoOrchestrator)
}

// TestResolve_AHostHomeRunOfARelocatableEngineRoutesToTheProjectRoot: an
// engine that declares a relocatable home delivers its session-home kinds
// beneath that home (its config dir, where it discovers them natively). A
// run whose binding selects the host home advises no such home — the real
// one is the engine's own, not ours to deliver into — so the plan routes
// every static kind to the project root instead; a session-home run of the
// same engine keeps the session home. The scratch root the cell always has
// is not a session home for such an engine.
func TestResolve_AHostHomeRunOfARelocatableEngineRoutesToTheProjectRoot(t *testing.T) {
	env := launchtest.Deps(t,
		launchtest.RelocatableHome(),
		launchtest.WithAgent("host", launchtest.EngineHome("host")),
		launchtest.WithAgent("session", launchtest.EngineHome("session")),
	)
	roots := func(agent string) map[present.Kind]present.RootKind {
		l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: agent, Mode: engine.Structured, Prompt: "x", WorkDir: env.Project})
		require.NoError(t, err)
		t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
		out := map[present.Kind]present.RootKind{}
		for _, item := range l.Plan.Static {
			out[item.Kind] = item.Root
		}
		require.NotEmpty(t, out, "the plan routes the package's kinds")
		return out
	}
	for kind, root := range roots("host") {
		require.Equal(t, present.RootProjectRoot, root, "kind %s: a host-home run has no session home to deliver into", kind)
	}
	for kind, root := range roots("session") {
		require.Equal(t, present.RootSessionHome, root, "kind %s: a session-home run delivers beneath the session home", kind)
	}
}
