package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// retiredContainerAxis is the undifferentiated container spelling the axis
// no longer has (rootless and rootful are the only container values).
const retiredContainerAxis launch.RuntimeAxis = "container"

// TestAgentRun_RefusesAnUnknownRuntimeAxis: a plan naming a runtime the axis
// does not have is refused at the verb with the parse's sentinel — never read
// as "not a container" and handed the loopback reach-back — and nothing
// launches.
func TestAgentRun_RefusesAnUnknownRuntimeAxis(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"boxed": {perm: "bypass", runtime: retiredContainerAxis}}, nil)
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "boxed", "go", "", "")
	require.ErrorIs(t, err, launch.ErrUnknownRuntimeAxis)
	assert.Equal(t, 0, sp.spawnCount(), "a refused plan never launches")
}

// TestReachURL_RefusesAnUnknownRuntimeAxis pins the chokepoint itself, which
// the owner run and the resume path reach without AgentRun.
func TestReachURL_RefusesAnUnknownRuntimeAxis(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	_, err := c.ReachURL(retiredContainerAxis)
	require.ErrorIs(t, err, launch.ErrUnknownRuntimeAxis)

	url, err := c.ReachURL(launch.RuntimeHost)
	require.NoError(t, err, "a known axis still resolves")
	assert.Equal(t, c.LoopbackURL(), url)
}
