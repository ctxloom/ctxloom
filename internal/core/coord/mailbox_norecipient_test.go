package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestQueueMail_RefusesAnUndrainableRecipient pins that mail with no
// recipient is refused at the durable boundary rather than appended.
//
// Role "" is undrainable by construction: agent_recv drains the CALLER's own
// harp, and no session has the empty harp, so a file written for it would be
// delivered to a directory nobody reads. The invariant belongs at the one
// place every sender funnels through.
func TestQueueMail_RefusesAnUndrainableRecipient(t *testing.T) {
	home := teeHome(t)
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	id, err := c.queueMail("child-a", "", KindUserInjected, "a digest nobody can ever read")
	assert.Error(t, err, "a message with no recipient must be refused, not queued")
	assert.Empty(t, id)
	if err != nil {
		assert.Contains(t, err.Error(), "recipient")
	}

	// PAYLOAD, not exit code: nothing reached any spool.
	assert.Equal(t, 0, c.pendingCount(""), "no pending mail may exist for role \"\"")
	assert.Empty(t, spoolDirsUnder(t, home), "the refused message must not have created a spool anywhere")

	// A recipient with a reader still queues, so the guard is a guard and not
	// a blanket refusal: the owner is one.
	id, err = c.queueMail("child-a", ownerIdentity().Harp, "message", "for a real session")
	assert.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Equal(t, 1, c.pendingCount(ownerIdentity().Harp))
}
