package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeerSend_RoutingAndDispositions characterizes peerSend's two routing
// branches and the disposition prose each returns, so splitting the function
// under the CCN-10 gate is provably behaviour-preserving. The
// existing conformance tests cover the unknown-recipient / no-parent /
// sibling-routing errors; these are the edges they leave: a caller that LOOKS
// like a child but has no run, and the exact prose each accepted send answers
// with (an agent reads this string to decide whether its message landed).
func TestPeerSend_RoutingAndDispositions(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	// A depth>0 identity with no run of its own has no parent to resolve.
	ghost := Identity{Harp: "ghost-harp", RunID: "no-such-run", Depth: 1}
	_, _, _, err := c.peerSend(ghost, ParentAddress, KindMessage, "anyone there", nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown sender")

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "report back", "", "")
	require.NoError(t, err)
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}

	// Child → parent: accepted by address AND by the parent's own harp.
	id, _, disposition, err := c.peerSend(child, ParentAddress, "result", "finding A", nil, "")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Equal(t, "sent to the coordinator", disposition)

	id, _, disposition, err = c.peerSend(child, ownerIdentity().Harp, "result", "finding B", nil, "")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Equal(t, "sent to the coordinator", disposition)

	// Owner → child: the disposition names the state the delivery observed.
	id, _, disposition, err = c.peerSend(ownerIdentity(), out.Harp, KindMessage, "next assignment", nil, "")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Contains(t, []string{
		"delivering as a new turn",
		"queued: the child has not started yet; it will drain its mailbox after its first turn",
		"queued mid-turn: delivered at the child's next turn boundary",
		"completed the child's waiting agent_recv",
	}, disposition, "the prose must be one of deliveryDisposition's, verbatim")
}
