package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/coordtest"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// buildHostCoordinator stands a real (production-spawner) coordinator up over
// a hermetic fixture with HOME scrubbed, serving loopback listeners — the
// same standup path `ctxloom run` uses.
func buildHostCoordinator(t *testing.T, subs map[string]agents.Agent) (*config.Config, *coord.Coordinator) {
	t.Helper()
	resetStrictness(t)
	cfg, root := delegationFixture(t, subs)
	runners := coordtest.NewRunners()
	t.Cleanup(runners.Close)
	c, err := coord.New(coord.Options{Spawner: spawn.New(nil, fixtureApp(t, cfg), root, runners.Starter), ProjectDir: root, StateDir: t.TempDir(), OwnerHarp: "coordinator-harp"})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)
	return cfg, c
}

// TestHostCoordinatorForSession_MintsTheOwnerCredential pins what the host
// gets back from standing its coordinator up: the credential the owner is
// registered under, which the coordinator identifies as the session owner at
// depth 0 — the identity the owner-owned run is minted under and the
// credential the host revokes on teardown. No environment rides anywhere:
// the owner's runner is stamped by StartOwnedRun with its own per-run trio.
func TestHostCoordinatorForSession_MintsTheOwnerCredential(t *testing.T) {
	_, c := buildHostCoordinator(t, map[string]agents.Agent{"worker": headlessAgent("p1")})

	token, err := c.RegisterSessionOwner("owner-harp")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	id, ok := c.Identify(token)
	require.True(t, ok, "the minted credential identifies the owner")
	assert.Equal(t, "owner-harp", id.Harp)
	assert.Equal(t, 0, id.Depth, "the owner is the root of the delegation tree")
}
