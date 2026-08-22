package coord

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendMailTurn_UndeliveredMessageComesBack pins the delivery half of the
// take/send pair. takeNextMail journals factMailConsumed AT TAKE — the
// message is durably gone from the mailbox before anything has been handed
// to the child — and sendTurn then had two paths that dropped it on the
// floor: a child whose input channel is already closed, and a coordinator
// shutting down. Neither requeued and neither said anything, so the message
// was consumed forever and delivered never.
//
// Both subtests drive the production pair exactly as onTurnBoundary does:
// takeNextMail, then sendMailTurn on an rt that cannot receive.
func TestSendMailTurn_UndeliveredMessageComesBack(t *testing.T) {
	const role = "child-undelivered"

	t.Run("the child's input channel is already closed", func(t *testing.T) {
		c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
		msg := takeOneForRole(t, c, role)

		// rt.in == nil: endChild has already closed and nil'd it.
		rt := &childRt{runID: "run-undelivered-a", harp: role, wake: make(chan struct{}, 1)}
		c.sendMailTurn(rt, msg)

		assertRequeued(t, c, role, msg)
	})

	t.Run("the coordinator is shutting down", func(t *testing.T) {
		c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
		msg := takeOneForRole(t, c, role)

		// A live input channel with nobody reading it, and a cancelled
		// baseCtx: sendTurn's shutdown arm wins the select. The stores are
		// still open, so the requeue itself is still durable.
		rt := &childRt{
			runID: "run-undelivered-b", harp: role,
			in:   make(chan agent.ChatMessage),
			wake: make(chan struct{}, 1),
		}
		c.cancel()
		c.sendMailTurn(rt, msg)

		assertRequeued(t, c, role, msg)
	})

	t.Run("no execution slot will ever come", func(t *testing.T) {
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
		// Cap of one, and the first run takes it: the second run's enqueue
		// finds nothing to claim, so wakeChild's acquireRunSlot is a real
		// blocking acquisition — and a cancelled baseCtx is what ends it.
		c := newTestCoordinatorCap(t, sp, nil, 1)
		plan, err := sp.Resolve(context.Background(), "worker")
		require.NoError(t, err)
		_, _, err = c.enqueueRun(ownerIdentity(), plan, "child-slot-holder", "brief", false, make(chan struct{}), 1)
		require.NoError(t, err)
		rt, _, err := c.enqueueRun(ownerIdentity(), plan, role, "brief", false, make(chan struct{}), 1)
		require.NoError(t, err)
		require.Equal(t, slotFree, rt.slot, "the cap is full, so this run holds no slot")

		c.setState(rt, StateIdle)
		id, _, err := c.queueMail("coordinator-harp", role, "note", "the body that must not be lost")
		require.NoError(t, err)
		c.cancel() // the blocking slot acquisition can now only fail

		c.wakeChild(rt)

		assertRequeued(t, c, role, Message{ID: id, From: "coordinator-harp", Kind: "note", Body: "the body that must not be lost"})
	})
}

// takeOneForRole queues one message for role and drains it through
// takeNextMail — leaving it durably CONSUMED and in the caller's hand, which
// is the state every sendMailTurn call starts from.
func takeOneForRole(t *testing.T, c *Coordinator, role string) Message {
	t.Helper()
	id, _, err := c.queueMail("coordinator-harp", role, "note", "the body that must not be lost")
	require.NoError(t, err)
	msg, ok, err := c.takeNextMail(role)
	require.NoError(t, err)
	require.True(t, ok, "the queued message must be takeable")
	require.Equal(t, id, msg.ID)
	require.Zero(t, c.pendingCount(role), "takeNextMail journals the consume at take: the mailbox is empty now")
	return msg
}

// assertRequeued proves the undelivered message is deliverable again, with
// its payload intact and under an id the fold will actually accept.
func assertRequeued(t *testing.T, c *Coordinator, role string, orig Message) {
	t.Helper()
	require.Equal(t, 1, c.pendingCount(role),
		"a message journaled as consumed and then never delivered must be re-queued, not dropped")
	back, ok, err := c.takeNextMail(role)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, orig.Body, back.Body, "the re-queued message keeps its payload")
	assert.Equal(t, orig.From, back.From, "the re-queued message keeps its sender")
	assert.Equal(t, orig.Kind, back.Kind, "the re-queued message keeps its kind")
	assert.NotEqual(t, orig.ID, back.ID,
		"mailFold dedupes queued facts on message_id, so a re-queue under the original id folds to nothing and the message stays lost")
}
