package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// reachAxes is every runtime axis a plan can carry: host and BOTH container
// ownership modes, which share no "any container" value — a site that judges
// only one of them compiles and stays green while the other loses its route.
var reachAxes = []launch.RuntimeAxis{launch.RuntimeHost, launch.RuntimeRootless, launch.RuntimeRootful}

// A delegated child's runner is handed the coordinator's own loopback URL on
// every axis: a container's cell re-mints it into the route its runtime
// dials, so anything else here is a URL the cell cannot re-mint. Asserted on
// the URL the runner actually received, once its StartRun reached the
// runner — which it can only do by dialing that URL.
func TestAgentRun_ChildRunnerIsHandedTheLoopbackReachOnEveryAxis(t *testing.T) {
	for _, axis := range reachAxes {
		t.Run(string(axis), func(t *testing.T) {
			resetStrictness(t)
			sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass", runtime: axis}}, nil)
			sp.bindHold = make(chan struct{})
			sp.bindEntered = make(chan struct{}, 1)
			c := newTestCoordinator(t, sp, nil)
			t.Cleanup(func() { close(sp.bindHold) })

			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
			require.NoError(t, err)
			require.Equal(t, axis, out.Runtime, "precondition: the plan's axis is the run's")
			select {
			case <-sp.bindEntered:
			case <-time.After(conformanceWait):
				t.Fatal("the child's StartRun never reached its runner")
			}

			require.NotEmpty(t, c.LoopbackURL(), "precondition: the coordinator is serving")
			assert.Equal(t, c.LoopbackURL(), sp.chat(0).RunnerEnv()[EnvCoordURL])
		})
	}
}

// An owned run's runner is handed the same reach as a delegated child's, on
// every axis.
func TestStartOwnedRun_RunnerIsHandedTheLoopbackReachOnEveryAxis(t *testing.T) {
	for _, axis := range reachAxes {
		t.Run(string(axis), func(t *testing.T) {
			c := newTestCoordinator(t, newFakeSpawner(t, nil, nil), nil)
			const ownerHarp = "owner-harp"
			token, err := c.RegisterSessionOwner(ownerHarp)
			require.NoError(t, err)
			owner, ok := c.Identify(token)
			require.True(t, ok)

			var gotEnv map[string]string
			starter := func(_ context.Context, spawnEnv map[string]string) (OwnedRunner, error) {
				gotEnv = spawnEnv
				return OwnedRunner{Kill: func() {}}, errStopBeforeDial
			}
			l := ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass")
			l.Axes.Runtime = axis

			_, err = c.StartOwnedRun(context.Background(), owner, ownerRun(l, false), starter, "hello")
			require.ErrorIs(t, err, errStopBeforeDial)
			require.NotEmpty(t, c.LoopbackURL(), "precondition: the coordinator is serving")
			assert.Equal(t, c.LoopbackURL(), gotEnv[EnvCoordURL])
		})
	}
}
