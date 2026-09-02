package coord

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bridgeTurnResult cleared rt.turnOutput BEFORE attempting the
// mailbox delivery (queueMail), with no retry or restore on failure. After a
// failed delivery the turn's own text existed nowhere: not in rt, not in the
// mailbox fold, not in the parent's view — the parent stays parked in
// agent_recv with no indication the report ever existed. A closed mail
// journal (mailbox_takefail_test.go's own pattern) is the
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

// The ONLY diagnostic for "this child's turn produced nothing" was
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

// The bridge is a FALLBACK for a child that filed no report, and it used to
// put the child's ENTIRE final message in the parent's mailbox. A coordinator's
// context is the scarce resource, and the moment this path fires is exactly
// when it is least affordable to flood it.
func TestBridgeTurnResult_BoundsTheBodyAndSaysWhereTheRestIs(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	huge := strings.Repeat("x", maxBridgedTurnBody*3)
	rt := &childRt{harp: "child-loud", parentHarp: "parent-loud", runID: "run-loud", oneshot: true, turnOutput: []string{huge}}

	c.bridgeTurnResult(rt)

	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor("parent-loud") })
	require.Len(t, pending, 1, "a oneshot child's bridged turn is one message")

	body := pending[0].Body
	assert.Less(t, len(body), len(huge),
		"an unbounded bridge hands the coordinator a whole model turn — the opposite of returning conclusions")
	assert.Contains(t, body, "truncated", "a caller must be told the body is partial, not handed a silent prefix")
	assert.Contains(t, body, "child-loud", "the notice must name where the full turn actually lives")
}

// The bound must not touch a body under it: a cap nobody hits should cost
// nothing, and a bridge that mangled short turns would be worse than no cap.
func TestBridgeTurnResult_ShortBodyIsUntouched(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	rt := &childRt{harp: "child-short", parentHarp: "parent-short", runID: "run-short", oneshot: true, turnOutput: []string{"a short verdict"}}
	c.bridgeTurnResult(rt)

	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor("parent-short") })
	require.Len(t, pending, 1)
	assert.Equal(t, "a short verdict", pending[0].Body, "a body under the cap must arrive verbatim")
}

// A NON-oneshot child that ends a turn without reporting has skipped its
// completion contract. The text is still delivered — losing it would be worse —
// but the parent is TOLD, in the only channel an agent can read.
func TestBridgeTurnResult_UnreportedTurn_TellsTheParentTheContractWasMissed(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	rt := &childRt{harp: "child-silent", parentHarp: "parent-silent", runID: "run-silent", turnOutput: []string{"some findings"}}
	c.bridgeTurnResult(rt)

	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor("parent-silent") })
	require.Len(t, pending, 2, "the parent gets BOTH the contract failure and the text the child did produce")

	var kinds []string
	var bodies string
	for _, m := range pending {
		kinds = append(kinds, m.Kind)
		bodies += m.Body + "\n"
	}
	assert.Contains(t, kinds, KindError, "the missed report must arrive as an error, not be inferable from silence")
	assert.Contains(t, kinds, KindResult, "the turn's own text must still reach the parent")
	assert.Contains(t, bodies, "without filing a report")
	assert.Contains(t, bodies, "some findings")
}

// A ONESHOT child is EXEMPT: the bridge is its delivery mechanism, so it never
// files a report and flagging every one would cry wolf on the normal case.
func TestBridgeTurnResult_OneshotTurn_IsNotFlaggedAsUnreported(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	rt := &childRt{harp: "child-oneshot", parentHarp: "parent-oneshot", runID: "run-oneshot", oneshot: true, turnOutput: []string{"the answer"}}
	c.bridgeTurnResult(rt)

	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor("parent-oneshot") })
	require.Len(t, pending, 1, "a oneshot child's bridged turn is its report, not a contract failure")
	assert.Equal(t, KindResult, pending[0].Kind)
}
