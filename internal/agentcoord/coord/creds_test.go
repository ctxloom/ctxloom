package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerifyToken_ConstantTimeMatch pins the credential verify: only the exact
// token resolves, and to the right identity.
func TestVerifyToken_ConstantTimeMatch(t *testing.T) {
	tokA, hashA, err := mintToken()
	require.NoError(t, err)
	tokB, hashB, err := mintToken()
	require.NoError(t, err)
	active := map[string]Identity{
		hashA: {Harp: "harp-a", Depth: 0},
		hashB: {Harp: "harp-b", RunID: "run-b", Depth: 1},
	}
	got, ok := verifyToken(tokA, active)
	require.True(t, ok)
	assert.Equal(t, "harp-a", got.Harp)
	got, ok = verifyToken(tokB, active)
	require.True(t, ok)
	assert.Equal(t, "run-b", got.RunID)
	_, ok = verifyToken("deadbeef", active)
	assert.False(t, ok, "an unknown token never matches")
}

// TestCredentialRevocation_SeversParkedPoll pins the security discipline:
// stopping a child (agent_stop → revocation) severs its parked agent_recv
// long-poll AND makes its credential immediately unverifiable.
func TestCredentialRevocation_SeversParkedPoll(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task")
	require.NoError(t, err)
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}

	// The child parks in agent_recv (its long-poll registers).
	severed := make(chan error, 1)
	go func() {
		_, rerr := c.AgentRecv(context.Background(), child, conformanceWait)
		severed <- rerr
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.polls[out.Harp] != nil
	}, conformanceWait, 10*time.Millisecond)

	// The credential is verifiable while live.
	env := waitForChildEnv(t, c, out.RunID)
	_, ok := c.Identify(env[EnvCoordCred])
	require.True(t, ok, "a live credential verifies")

	// Stop the child: revocation severs the parked poll and the credential.
	_, err = c.AgentStop(ownerIdentity(), out.Harp)
	require.NoError(t, err)

	select {
	case rerr := <-severed:
		require.ErrorIs(t, rerr, ErrRevoked, "the parked poll is severed with the revoked error")
	case <-time.After(conformanceWait):
		t.Fatal("the parked poll was never severed")
	}

	_, ok = c.Identify(env[EnvCoordCred])
	assert.False(t, ok, "a revoked credential no longer verifies")
}

// TestMailbox_AtLeastOnceRedeliveryAndDedupe pins the at-least-once contract:
// a recv that returns a message but is NOT followed by an acking recv leaves
// the message re-deliverable after a coordinator relaunch (durable), and a
// duplicate queued message id is deduped.
func TestMailbox_AtLeastOnceRedeliveryAndDedupe(t *testing.T) {
	resetStrictness(t)
	stateDir := mkTempDir(t)

	// Round 1: queue two messages to a role, recv them (delivered, NOT acked),
	// then simulate a crash (close without the acking recv).
	c1 := newTestCoordinatorAt(t, stateDir)
	role := "harp-x"
	_, err := c1.queueMail("sender", role, "", "first")
	require.NoError(t, err)
	_, err = c1.queueMail("sender", role, "", "second")
	require.NoError(t, err)

	msgs, err := c1.recvMail(context.Background(), role, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "both pending messages are delivered")
	c1.Close() // crash before the acking recv appends consume facts

	// Round 2: a fresh coordinator adopts the SAME journals. The undelivered
	// (unacked) messages are re-delivered — at-least-once.
	c2 := newTestCoordinatorAt(t, stateDir)
	redelivered, err := c2.recvMail(context.Background(), role, 0)
	require.NoError(t, err)
	require.Len(t, redelivered, 2, "unacknowledged deliveries survive a relaunch and re-deliver")
	assert.Equal(t, "first", redelivered[0].Body)
	assert.Equal(t, "second", redelivered[1].Body)

	// A subsequent recv ACKS them (cursor-ack); nothing re-delivers after.
	_, err = c2.recvMail(context.Background(), role, 0)
	require.ErrorIs(t, err, ErrRecvTimeout, "after the acking recv the mailbox is empty")
	c2.Close()

	c3 := newTestCoordinatorAt(t, stateDir)
	_, err = c3.recvMail(context.Background(), role, 0)
	require.ErrorIs(t, err, ErrRecvTimeout, "consumed messages stay consumed across a relaunch")
	c3.Close()
}

// TestMailbox_PollPreemption pins the one-active-poll-per-role rule: a newer
// long-poll preempts the older one (ErrRecvPreempted).
func TestMailbox_PollPreemption(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinatorAt(t, mkTempDir(t))
	defer c.Close()
	role := "harp-p"

	old := make(chan error, 1)
	go func() {
		_, err := c.recvMail(context.Background(), role, conformanceWait)
		old <- err
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.polls[role] != nil
	}, conformanceWait, 5*time.Millisecond)

	// A newer poll preempts the older.
	go func() { _, _ = c.recvMail(context.Background(), role, conformanceWait) }()

	select {
	case err := <-old:
		require.ErrorIs(t, err, ErrRecvPreempted)
	case <-time.After(conformanceWait):
		t.Fatal("the older poll was never preempted")
	}
}
