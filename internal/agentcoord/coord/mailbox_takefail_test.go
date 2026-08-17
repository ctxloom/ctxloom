package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- a journal failure must not make a message invisible ----------
//
// NOTE on assertion style: this package's Coordinator.Close runs in
// t.Cleanup, and a require.* FailNow inside a coord test deadlocks it (the
// cute-brink hazard — a silent multi-minute hang, since test-pkg buffers).
// Every check here is assert + return.

// takeNextMail RESERVES the message id under the lock and only then appends
// the mail-consumed fact. On the append's error path it returned
// (Message{}, false) without unreserving and without surfacing the error, so:
//
//   - the id stayed in c.delivered[role] forever — nothing else clears it
//     (severChan un-reserves only ch.pushed, ackDelivered only ids it copied),
//     so undeliveredLocked filters it out and the message is permanently
//     invisible; and
//   - the sole caller (resumeChild) read ok==false as "no mail", skipped the
//     first turn, and drove the child with NO PROMPT — markAttached,
//     driveChild, exit 0, roster "executing", zero bytes delivered. That is
//     this project's characteristic silent no-op, in the one place the
//     container-delegation incident already proved it is invisible to every
//     cheap signal.
//
// A closed journal is the deterministic shape of that failure, and a real one
// (the coordinator shutting down while a relaunch is in flight).
func TestTakeNextMail_JournalFailure_DoesNotStrandTheMessage(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-a"
	_, _, err := c.queueMailPayloadID("m1", "parent", role, "task", "do the thing", nil, "")
	if !assert.NoError(t, err) {
		return
	}
	if !assert.Equal(t, 1, c.pendingCount(role), "precondition: the message is deliverable") {
		return
	}

	// Break the append path the way a shutdown does.
	if !assert.NoError(t, c.mail.Close()) {
		return
	}

	msg, ok, terr := c.takeNextMail(role)
	assert.False(t, ok, "a message that could not be journaled was not delivered")
	assert.Empty(t, msg.Body)
	assert.Error(t, terr, "the journal failure must reach the caller, not be swallowed")

	assert.Equal(t, 1, c.pendingCount(role),
		"a failed take must release its reservation — the message must stay deliverable, not vanish")
}
