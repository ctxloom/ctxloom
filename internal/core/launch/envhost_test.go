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

// The cell is asked for the binding's env_host and env keys as declared: the
// cells adapter's host environment is what applies them.
func TestResolve_EnvHost_TheBindingsDeclarationReachesTheCell(t *testing.T) {
	env := launchtest.Deps(t,
		launchtest.WithAgent("silent"),
		launchtest.WithAgent("narrow", launchtest.EnvHost(false, "GITHUB_TOKEN")),
	)
	want := map[string]agents.EnvHost{"silent": {}, "narrow": {Curated: true, Env: []string{"GITHUB_TOKEN"}}}
	for agent, want := range want {
		l, err := launch.Resolve(context.Background(), env.Deps, resolveSource(env, agent))
		require.NoError(t, err, agent)
		t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
		require.Equal(t, want, env.LastCellRequest().EnvHost, agent)
	}
}

// A declaration that would not do what it says is refused before any cell
// is prepared: env names with env_host on, and an env entry that is not a
// bare name.
func TestResolve_EnvHost_AnInvalidDeclarationIsRefused(t *testing.T) {
	for agent, c := range map[string]struct {
		opt  launchtest.AgentOption
		want error
	}{
		"loose":  {launchtest.EnvHost(true, "GITHUB_TOKEN"), agents.ErrEnvWithEnvHost},
		"valued": {launchtest.EnvHost(false, "GITHUB_TOKEN=abc"), agents.ErrEnvNotBareName},
	} {
		env := launchtest.Deps(t, launchtest.WithAgent(agent, c.opt))
		_, err := launch.Resolve(context.Background(), env.Deps, resolveSource(env, agent))
		require.ErrorIs(t, err, c.want, agent)
	}
}

func resolveSource(env launchtest.Env, agent string) launch.Source {
	return launch.Source{Identity: env.Identity, Agent: agent, Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project}
}
