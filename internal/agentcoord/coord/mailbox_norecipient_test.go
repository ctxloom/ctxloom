package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestQueueMail_RefusesAnUndrainableRecipient pins that mail with no
// recipient is refused at the durable boundary rather than appended.
//
// Role "" is undrainable by construction: agent_recv drains the CALLER's own
// harp, and no session has the empty harp, so such a fact is fsynced into the
// mailbox journal, replayed on every relaunch, and read by nobody — forever.
// Three of the four production senders (bridgeTurnResult, the launch-gate
// error notice, relayApproval) each guard their recipient locally; the
// coordinator's user-injection mirror does not, which is exactly why the
// invariant belongs at the one place every one of them funnels through.
func TestQueueMail_RefusesAnUndrainableRecipient(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	id, completed, err := c.queueMail("child-a", "", KindUserInjected, "a digest nobody can ever read")
	assert.Error(t, err, "a message with no recipient must be refused, not queued")
	assert.Empty(t, id)
	assert.False(t, completed)
	if err != nil {
		assert.Contains(t, err.Error(), "recipient")
	}

	// PAYLOAD, not exit code: nothing reached the fold and nothing reached the
	// journal file.
	assert.Equal(t, 0, c.pendingCount(""), "no pending mail may exist for role \"\"")
	raw, rerr := os.ReadFile(filepath.Join(c.stateDir, "mailbox.jsonl"))
	if assert.NoError(t, rerr) {
		assert.NotContains(t, string(raw), factMailQueued,
			"the refused message must not be durably appended")
		assert.Empty(t, strings.TrimSpace(string(raw)))
	}

	// A named recipient still queues, so the guard is a guard and not a
	// blanket refusal.
	id, _, err = c.queueMail("child-a", "child-b", "note", "for a real session")
	assert.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Equal(t, 1, c.pendingCount("child-b"))
}
