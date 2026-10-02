package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// The cell is asked for the binding's host_env as declared: the cells
// adapter's host environment is what applies it.
func TestResolve_HostEnv_TheBindingsDeclarationReachesTheCell(t *testing.T) {
	curated := agents.HostEnv{Curated: true, Passthrough: []string{"GITHUB_TOKEN"}}
	env := launchtest.Deps(t,
		launchtest.WithAgent("silent"),
		launchtest.WithAgent("narrow", launchtest.HostEnv(curated)),
	)
	for agent, want := range map[string]agents.HostEnv{"silent": {}, "narrow": curated} {
		l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: agent, Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
		require.NoError(t, err, agent)
		t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
		require.Equal(t, want, env.LastCellRequest().HostEnv, agent)
	}
}

// A passthrough with no opt-in is refused before any cell is prepared.
func TestResolve_HostEnv_PassthroughWithoutCuratedIsRefused(t *testing.T) {
	env := launchtest.Deps(t, launchtest.WithAgent("loose", launchtest.HostEnv(agents.HostEnv{Passthrough: []string{"GITHUB_TOKEN"}})))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "loose", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, agents.ErrHostEnvPassthroughUncurated)
}
