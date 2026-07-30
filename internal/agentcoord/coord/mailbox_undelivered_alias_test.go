package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U023-F16: undeliveredLocked filtered IN PLACE (`out := pending[:0]`) into
// whatever mailFold.pendingFor handed back, so a read of the mailbox rewrote the
// slice it was reading. That was safe only because pendingFor happens to return
// a copy — an invariant declared in another file, about a different type, with
// nothing linking the two. Make pendingFor return its backing array (an
// ordinary-looking optimisation) and this read path starts reordering and
// truncating the fold's live queue, dropping messages with every filtered read.
//
// The pin is on the fold's queue being INTACT after a filtered read, so it holds
// whatever pendingFor's copy semantics are.
func TestUndeliveredLocked_DoesNotDisturbTheFoldsQueue(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	role := "child-alias"

	var ids []string
	for _, body := range []string{"first", "second", "third"} {
		id, _, err := c.queueMail("owner", role, "task", body)
		require.NoError(t, err)
		ids = append(ids, id)
	}

	// Reserve the MIDDLE message: the filter must therefore skip an element and
	// keep writing after it — the shape that compacts in place.
	c.mu.Lock()
	c.delivered[role] = []string{ids[1]}
	got := c.undeliveredLocked(role)
	c.mu.Unlock()

	require.Len(t, got, 2, "the reserved message is filtered out")
	assert.Equal(t, ids[0], got[0].ID)
	assert.Equal(t, ids[2], got[1].ID)

	var after []Message
	c.mail.View(func() { after = c.mailF.pendingFor(role) })
	afterIDs := make([]string, len(after))
	for i, m := range after {
		afterIDs[i] = m.ID
	}
	assert.Equal(t, ids, afterIDs,
		"reading the mailbox must leave the fold's pending queue exactly as it was, in arrival order")

	// And the reserved message is still genuinely deliverable once un-reserved:
	// an in-place filter that clobbered the queue would have lost it.
	c.unreserve(role, []string{ids[1]})
	assert.Equal(t, 3, c.pendingCount(role))
}
