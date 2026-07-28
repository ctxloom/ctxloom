package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U020-F03: bridgeTurnResult cleared rt.turnOutput BEFORE attempting the
// mailbox delivery (queueMail), with no retry or restore on failure. After a
// failed delivery the turn's own text existed nowhere: not in rt, not in the
// mailbox fold, not in the parent's view — the parent stays parked in
// agent_recv with no indication the report ever existed. A closed mail
// journal (mailbox_takefail_test.go's own pattern for U023-F03) is the
// deterministic shape of that failure.
func TestBridgeTurnResult_JournalFailure_RestoresAccumulatorForRetry(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	rt := &childRt{harp: "child-a", parentHarp: "parent-a", turnOutput: []string{"the turn's actual output"}}

	require.NoError(t, c.mail.Close())

	c.bridgeTurnResult(rt)

	c.mu.Lock()
	got := append([]string{}, rt.turnOutput...)
	c.mu.Unlock()
	assert.Equal(t, []string{"the turn's actual output"}, got,
		"a failed mailbox delivery must not silently discard the turn's output — it must be restored so the "+
			"next turn boundary retries delivering it, instead of the report existing nowhere")
}

// U020-F04: the ONLY diagnostic for "this child's turn produced nothing" was
// a clidiag.Warn — coordinator-process stderr, a channel the parent (an
// agent whose sole input is its mailbox) cannot read. Under the
// runtime:container prompt-delivery defect this fires every turn while every
// cheap signal stays green, and the parent observes an indefinitely silent,
// "executing" child with no diagnostic reaching it at all.
func TestBridgeTurnResult_EmptyTurn_NotifiesParentMailbox(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	rt := &childRt{harp: "child-empty", parentHarp: "parent-of-empty", runID: "run-empty"}

	c.bridgeTurnResult(rt) // turnOutput is nil: text == "", the empty-turn path

	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor("parent-of-empty") })
	if !assert.Len(t, pending, 1, "an empty turn must notify the parent's mailbox, not just coordinator stderr") {
		return
	}
	assert.Equal(t, "error", pending[0].Kind)
	assert.Contains(t, pending[0].Body, "no output")
	assert.Contains(t, pending[0].Body, "child-empty")
}
