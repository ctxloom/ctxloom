package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHome_IdentityArrivesOnTheLaunch_NotTheEnvironment: a hosted run's
// Home dials with the reach-back trio alone and learns WHICH run it is —
// the harp its spool is named by, the depth its turn report is gated on —
// from the Launch the coordinator's StartRun carries, bound once by the
// engine host as it drives. Until then the Home has no spool to sweep and
// sweeps nothing.
func TestHome_IdentityArrivesOnTheLaunch_NotTheEnvironment(t *testing.T) {
	h, err := NewHome(context.Background(), HomeConfig{URL: "http://127.0.0.1:1/mcp", Token: "t", RunID: "run-1", Harness: "mock", Version: "test"})
	require.NoError(t, err, "a hosted run's Home needs no harp to dial: its identity is on the launch")
	t.Cleanup(func() { h.Close(0, "") })
	assert.Equal(t, "", h.Harp(), "unbound until the launch arrives")
	h.SweepSpoolIn() // nothing to sweep, and no spool is invented

	h.BindIdentity(Identity{Harp: "child-harp-1", RunID: "run-1", Depth: 1})
	assert.Equal(t, "child-harp-1", h.Harp())
	assert.Equal(t, 1, h.Depth())

	h.BindIdentity(Identity{Harp: "another", Depth: 2})
	assert.Equal(t, "child-harp-1", h.Harp(), "identity binds ONCE; a second launch cannot rename a live runner's spool")
}

// TestHome_ThePluginArmsOwnerBindsFromItsConfig: the session owner's own
// runner receives no StartRun (the plugin-hosted arm), so its harp rides
// HomeConfig from the process env — bound at dial, depth 0.
func TestHome_ThePluginArmsOwnerBindsFromItsConfig(t *testing.T) {
	h, err := NewHome(context.Background(), HomeConfig{URL: "http://127.0.0.1:1/mcp", Token: "t", Harness: "mock", Version: "test", Harp: "owner-harp"})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	assert.Equal(t, "owner-harp", h.Harp())
	assert.Equal(t, 0, h.Depth())
}
