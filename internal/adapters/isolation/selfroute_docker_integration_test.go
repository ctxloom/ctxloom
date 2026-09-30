//go:build docker_integration

package isolation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/containerprobe"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// TestSelfRoute_TakenInsideADaemonContainer is the docker-outside-of-docker
// canary: run inside a container of the daemon it drives (CI's job container,
// with the host's socket mounted), this process must be identified as that
// container and its runners routed onto that container's network — so a
// green docker-integration lane cannot mean "fell back to something else".
// Off-container there is nothing to identify, and it skips.
func TestSelfRoute_TakenInsideADaemonContainer(t *testing.T) {
	if len(containerprobe.SelfIDCandidates()) == 0 {
		dockergate.SkipCapability(t, "this process shows no trace of running in a container")
	}
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the docker-outside-of-docker self-route test")
	d, _ := newDockerRuntime(runtimeReachable)
	require.NotNil(t, d.self, "this process runs in a container but the daemon it drives did not confirm it as one of its own")
	route, err := d.reachRoute(context.Background())
	require.NoError(t, err)
	if d.self.hostNet {
		assert.Empty(t, route.network, "a host-network container takes the host's own routes")
		return
	}
	assert.Equal(t, d.self.network.name, route.network)
	assert.Equal(t, d.self.network.ip, route.dial)
	assert.Equal(t, d.self.network.ip, route.listen.Addr)
}
