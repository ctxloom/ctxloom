package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	kindHuman       = InitiatorHuman
	kindAgent       = InitiatorAgent
	kindUnspecified = InitiatorUnspecified
)

// humanInitiator is the viewer/terminal's initiator, spelled once.
func humanInitiator() ControlInitiator { return ControlInitiator{Kind: kindHuman} }

// TestControlInitiator_Validate pins the Kind/Harp pairing a bool used to make
// unrepresentable-by-accident. The unrecognised-kind row is the load-bearing
// one: an initiator this build does not know must be REFUSED, never defaulted
// into the narrower branch's privileges.
func TestControlInitiator_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ControlInitiator
		ok   bool
	}{
		{"human without harp", ControlInitiator{Kind: kindHuman}, true},
		{"human WITH a harp is malformed", ControlInitiator{Kind: kindHuman, Harp: "parent"}, false},
		{"agent naming itself", ControlInitiator{Kind: kindAgent, Harp: "parent"}, true},
		{"agent without a harp cannot be ownership-checked", ControlInitiator{Kind: kindAgent}, false},
		{"unspecified is not an initiator", ControlInitiator{Kind: kindUnspecified}, false},
		{"a kind this build does not know", ControlInitiator{Kind: ControlInitiatorKind("99")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}

// TestControlSteer_RefusesSelfTarget is the hazard-2 fence. In the owner-run
// topology the coordinating session's own credential is depth 1 with parent ==
// its own harp, so without this guard a session passes its own ownership check
// and can steer itself in a loop with no floor.
func TestControlSteer_RefusesSelfTarget(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.ControlSteer(context.Background(), ControlInitiator{Kind: kindAgent, Harp: out.Harp}, out.Harp, "steer myself")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot control itself")
	assert.Zero(t, c.pendingCount(out.Harp), "a refused steer must queue nothing anywhere")
}

// TestControlSteer_AgentInitiatorControlsOnlyItsOwnChildren pins guard 3's
// AGENT arm, and TestControlSteer_RefusesUnrecognisedInitiator pins that the
// switch is exhaustive rather than defaulting.
func TestControlSteer_AgentInitiatorControlsOnlyItsOwnChildren(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.ControlSteer(context.Background(), ControlInitiator{Kind: kindAgent, Harp: "some-other-parent"}, out.Harp, "not yours")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the parent")
	assert.Zero(t, c.pendingCount(out.Harp))
}

func TestControlSteer_RefusesUnrecognisedInitiator(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.ControlSteer(context.Background(), ControlInitiator{Kind: ControlInitiatorKind("99"), Harp: "x"}, out.Harp, "who am I")
	require.Error(t, err)
	assert.Zero(t, c.pendingCount(out.Harp),
		"an initiator this build does not recognise must not fall into the human branch and deliver anyway")
}

// TestCapUnavailable_IsBothAStatusAndACause: the refusal has to satisfy two
// callers at once — a wire caller reading a gRPC code, and an in-process caller
// routing on the cause. Matching on the code alone cannot distinguish a
// capability gap from the other FAILED_PRECONDITIONs, and matching on prose is
// not a contract. The code is the wire adapter's (coordgrpc.StatusFromErr,
// pinned by its own table); the cause is the error's own.
func TestCapUnavailable_IsACause(t *testing.T) {
	err := capUnavailable("run %q does not offer %q", "child-a", "pause")
	assert.True(t, errors.Is(err, ErrCapabilityUnavailable))
	assert.False(t, errors.Is(err, ErrRecvTimeout))
	assert.Contains(t, err.Error(), "pause")
}
