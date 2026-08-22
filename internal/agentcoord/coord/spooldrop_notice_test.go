package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// THE DROPPED REPORT (appealing-matrix). A child writes its report into out/,
// the coordinator's sweep cannot route it, and the file is consumed. Before
// this, the ONLY parties told were the sender — by mail addressed to a session
// that has usually already exited — and clidiag on a runner's stderr. The
// parent, the one party whose work depends on the answer, sat in agent_recv
// until it timed out and concluded the child never reported.
//
// Both tests below assert receipt AT THE PARENT and assert the child's OWN
// TEXT survived the drop, because "a notice arrived" is satisfied by a bare
// "something was lost" that sends a coordinator to interrogate an agent which
// no longer exists.

// awaitParentNotice drains the owner's mailbox until a message whose body
// contains want arrives, and returns it.
func awaitParentNotice(t *testing.T, c *Coordinator, want string) Message {
	t.Helper()
	got := recvWhere(t, c, func(m Message) bool { return strings.Contains(m.Body, want) }, conformanceWait)
	require.NotEmpty(t, got, "the parent was never told about the dropped message (wanted a body containing %q)", want)
	return got[0]
}

// assertNoSecondNotice forces another sweep and fails if the same notice is
// produced again — the drop must be terminal, not re-announced on every
// reconciliation pass for the life of the process.
func assertNoSecondNotice(t *testing.T, c *Coordinator, harp, want string) {
	t.Helper()
	c.spoolReactor.mark(harp)
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		msgs, err := recvOwner(c)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			assert.NotContains(t, m.Body, want, "the drop notice must be sent once, not re-sent on every sweep")
		}
	}
}

// recvOwner is a single short non-parking drain of the owner's mailbox.
func recvOwner(c *Coordinator) ([]Message, error) {
	return c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
}

// TestSpoolDrop_UnmappableKindTellsTheParentAndCarriesTheText covers the
// mailFromSpool half of the drop: a file that parses as a message but carries
// a kind this build's closed vocabulary cannot resolve. There is no Message to
// reply to the sender with at all on that path, so before this the drop was
// invisible to everyone.
func TestSpoolDrop_UnmappableKindTellsTheParentAndCarriesTheText(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	const finding = "the mutation was RED and the gate exited 0"
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	ref, err := w.Write(&spool.Message{
		Kind:     "a-kind-this-build-does-not-know",
		FromHarp: out.Harp,
		To:       ParentAddress,
		Body:     finding,
	})
	require.NoError(t, err)
	c.spoolReactor.mark(out.Harp)

	notice := awaitParentNotice(t, c, finding)
	assert.Equal(t, KindError, notice.Kind, "an undelivered-message notice is an error, not a result")
	assert.Equal(t, out.Harp, notice.From, "the text below the header is the child's own words, so the child is the author")
	assert.Contains(t, notice.Body, "UNDELIVERED", "the parent must be able to tell this apart from the report it was waiting for")
	assert.Contains(t, notice.Body, ref.Name, "the notice must name the file, so an operator can go and read it")

	// LOUD and TERMINAL: counted, and gone from out/.
	assert.GreaterOrEqual(t, c.SpoolDeliveryStats().Failed, uint64(1), "a dropped message must be counted, never silently skipped")
	require.Eventually(t, func() bool { return len(spoolEntries(t, out.Harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond,
		"the undeliverable entry must not sit in out/ being re-read forever")
	assertNoSecondNotice(t, c, out.Harp, finding)
}

// TestSpoolDrop_RefusedRoutingTellsTheParentNotOnlyTheSender covers the
// peerSend half: a well-formed message the routing chokepoint refuses. Here
// the sender IS told (replySpoolRefusal), which is exactly what made the loss
// look handled — that reply is queued to the child, and a child that wrote its
// last message and exited never reads it.
func TestSpoolDrop_RefusedRoutingTellsTheParentNotOnlyTheSender(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	const finding = "sibling-addressed findings that must not evaporate"
	w, err := spool.NewWriter(spool.NewHomeMapper(), out.Harp, spool.DirOut, out.Harp)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{
		Kind:     KindResult,
		FromHarp: out.Harp,
		// Hub-and-spoke: a child may address only its parent. peerSend
		// refuses this, which is correct — and used to end the message.
		To:   "a-sibling-harp",
		Body: finding,
	})
	require.NoError(t, err)
	c.spoolReactor.mark(out.Harp)

	notice := awaitParentNotice(t, c, finding)
	assert.Equal(t, KindError, notice.Kind)
	assert.Contains(t, notice.Body, "a-sibling-harp", "the notice must say who the sender believed it was writing to")
	assert.Contains(t, notice.Body, "could not be routed", "the parent must learn WHY, not merely that something happened")

	require.Eventually(t, func() bool { return len(spoolEntries(t, out.Harp, spool.DirOut)) == 0 }, conformanceWait, 10*time.Millisecond,
		"the refused entry must reach a terminal state")
	assertNoSecondNotice(t, c, out.Harp, finding)
}
