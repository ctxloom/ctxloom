package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
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

// TestCredentialRevocation_RevokesTheChildsCredential: agent_stop revokes
// the child's run credential, so a runner that outlives its stop can no
// longer speak as that run.
func TestCredentialRevocation_RevokesTheChildsCredential(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)

	// The credential is verifiable while live.
	env := waitForChildEnv(t, c, out.RunID)
	_, ok := c.Identify(env[EnvCoordCred])
	require.True(t, ok, "a live credential verifies")

	_, err = c.AgentStop(ownerIdentity(), out.Harp, "", 0)
	require.NoError(t, err)

	_, ok = c.Identify(env[EnvCoordCred])
	assert.False(t, ok, "a revoked credential no longer verifies")
}

// TestMailbox_AtLeastOnceRedelivery pins the at-least-once contract across a
// coordinator relaunch: mail a reader claimed but never acknowledged (a
// turn-start hook that died between claim and ack) is delivered again from
// the same spool, and mail that was acknowledged stays consumed.
func TestMailbox_AtLeastOnceRedelivery(t *testing.T) {
	resetStrictness(t)
	stateDir := mkTempDir(t)

	// Round 1: queue two messages to the owner, claim them, and never
	// acknowledge (the reader crashed); the coordinator goes down too.
	c1 := newTestCoordinatorAt(t, stateDir)
	role := ownerIdentity().Harp
	_, err := c1.queueMail("sender", role, KindMessage, "first")
	require.NoError(t, err)
	_, err = c1.queueMail("sender", role, KindMessage, "second")
	require.NoError(t, err)
	claimed, err := spool.Claim(c1.mapper, role)
	require.NoError(t, err)
	require.Len(t, claimed.Entries, 2, "both pending messages are claimed")
	c1.Close()

	// Round 2: a fresh coordinator over the SAME state: the claimed but
	// unacknowledged messages are delivered again.
	c2 := newTestCoordinatorAt(t, stateDir)
	redelivered, err := spoolMail(t, c2, role, 0)
	require.NoError(t, err)
	require.Len(t, redelivered, 2, "unacknowledged deliveries survive a relaunch and re-deliver")
	assert.Equal(t, "first", redelivered[0].Body)
	assert.Equal(t, "second", redelivered[1].Body)

	// That read acknowledged them; nothing re-delivers after.
	_, err = spoolMail(t, c2, role, 0)
	require.ErrorIs(t, err, errNoOwnerMail, "after the acknowledging read the spool is empty")
	c2.Close()

	c3 := newTestCoordinatorAt(t, stateDir)
	_, err = spoolMail(t, c3, role, 0)
	require.ErrorIs(t, err, errNoOwnerMail, "consumed messages stay consumed across a relaunch")
	c3.Close()
}
