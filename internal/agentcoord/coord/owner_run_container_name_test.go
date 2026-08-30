package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// containerNameFromRoster scans the live roster for runID's AgentIdentity —
// the shared assertion body for the two tests below.
func containerNameFromRoster(t *testing.T, c *Coordinator, runID string) string {
	t.Helper()
	var found *agentcoordpb.ListRunsResult_RunInfo
	for _, r := range c.ListRuns(true, "").GetRuns() {
		if r.GetRunId() == runID {
			found = r
		}
	}
	require.NotNil(t, found, "the run must appear in the roster")
	return found.GetAgent().GetContainerName()
}

// TestStartOwnedRun_SurfacesContainerNameOnRoster pins fragile-volatile: a
// container-runtime owner-owned run's resolved container name
// (isolation.RunnerHandle.Name — stood in for here since this test never
// launches a real container) must reach the roster projection
// (AgentIdentity.ContainerName via listRunsSnapshot in consumer.go) — the
// human-usable `docker logs -f`/`docker attach` handle when tmux is
// unavailable.
func TestStartOwnedRun_SurfacesContainerNameOnRoster(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp-container"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	const wantName = "ctxloom-owner-harp-container-x7q2"
	sc := &scriptedChat{}
	starter, started := ownerRunStarterNamed(ctx, sc, "claude-code", wantName)

	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "hello owner run")
	require.NoError(t, err)
	require.True(t, *started, "StartOwnedRun must launch the runner via the starter")

	assert.Equal(t, wantName, containerNameFromRoster(t, c, outcome.RunID),
		"a container-runtime run's resolved name must reach the roster")
}

// TestStartOwnedRun_ContainerNameEmptyForHostRun is the negative pin: an
// owner-owned run whose starter reports no container name (the host-runtime
// shape isolation.RunnerHandle.Name always takes) must show an empty
// ContainerName on the roster — never fabricated.
func TestStartOwnedRun_ContainerNameEmptyForHostRun(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp-host"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	sc := &scriptedChat{}
	starter, started := ownerRunStarter(ctx, sc, "claude-code")

	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "claude-code",
		Label:      "fast",
		Model:      "sonnet",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter, "hello owner run")
	require.NoError(t, err)
	require.True(t, *started)

	assert.Equal(t, "", containerNameFromRoster(t, c, outcome.RunID),
		"a host-runtime run must never carry a fabricated container name")
}
