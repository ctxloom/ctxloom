package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// Tests for the OWNER HOP of the mail-plane cutover: the session owner is a
// spool recipient, and its in/ is drained in-process by AgentRecv.
//
// The owner is the highest-traffic recipient on the bus — every child's FINAL
// report lands there — so every test below asserts the CONTENT arrived, never
// that a call returned true.

// recvNothing performs one bounded owner receive and asserts it returned no
// message whose body is body.
func recvNothing(t *testing.T, c *Coordinator, body string) {
	t.Helper()
	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 50*time.Millisecond)
	if err != nil {
		require.ErrorIs(t, err, ErrRecvTimeout)
	}
	for _, m := range msgs {
		assert.NotEqual(t, body, m.Body, "a message acked by a prior receive must never be delivered again")
	}
}

// TestSpoolOwner_FinalReportReachesTheOwnerThroughTheSpool is the row's
// settling proof: a child's FINAL report arrives at the owner's agent_recv
// FROM A FILE in the owner's own in/, and no mailbox fact was ever journaled
// for it. Make spoolDeliverTo refuse the owner again and the mailbox-twin
// assertion goes red.
func TestSpoolOwner_FinalReportReachesTheOwnerThroughTheSpool(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: the deliverable"))

	got := recvBody(t, c, "FINAL: the deliverable", conformanceWait)
	require.NotEmpty(t, got, "the child's FINAL report must reach the owner's agent_recv")
	assert.Equal(t, KindReport, got[0].Kind)
	assert.Equal(t, out.Harp, got[0].From, "the notice is authored by the child that filed it")

	// THE FILE IS THE MESSAGE: it is in the owner's in/ (delivered, not yet
	// acked) and nowhere in the mailbox fold.
	entry, ok := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirIn, "FINAL: the deliverable")
	require.True(t, ok, "the delivered report must still sit in the owner's in/ until the next receive acks it")
	assert.Equal(t, got[0].ID, entry.Message.OriginID, "the mailbox id the owner saw is the file's origin id")
	assert.Empty(t, mailboxEverQueued(c), "under the cutover the owner's mail must have NO mailbox twin")
}

// TestSpoolOwner_ChildSendRidesTheFileIntoAgentRecv pins the ordinary
// child->parent send end to end over files: out/ of the child, routed by the
// coordinator into in/ of the owner, read by agent_recv.
func TestSpoolOwner_ChildSendRidesTheFileIntoAgentRecv(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")

	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "a finding", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	got := recvBody(t, c, "a finding", conformanceWait)
	require.NotEmpty(t, got)
	assert.Equal(t, out.Harp, got[0].From)
	assert.Equal(t, KindResult, got[0].Kind)
	_, inOwnerSpool := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirIn, "a finding")
	assert.True(t, inOwnerSpool, "the routed message must be a file in the owner's in/")
	assert.Empty(t, mailboxEverQueued(c), "no mailbox twin")
}

// TestSpoolOwner_AckIsConsumeOnNextRecv pins at-least-once for the owner:
// a delivered file stays in in/ until a SUBSEQUENT receive proves the harness
// took the batch, at which point it is renamed into in/consumed/ — and it is
// never delivered twice inside one process.
func TestSpoolOwner_AckIsConsumeOnNextRecv(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: once"))
	got := recvBody(t, c, "FINAL: once", conformanceWait)
	require.NotEmpty(t, got)

	_, stillIn := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirIn, "FINAL: once")
	require.True(t, stillIn, "delivered but unacked: the file must still be in in/")

	// The next receive is the ack — and returns the message no second time.
	recvNothing(t, c, "FINAL: once")
	_, consumed := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirInConsumed, "FINAL: once")
	assert.True(t, consumed, "the ack is the consume-rename into in/consumed/")
	_, stillIn = spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirIn, "FINAL: once")
	assert.False(t, stillIn, "an acked file must have left in/")
	recvNothing(t, c, "FINAL: once")
}

// TestSpoolOwner_ParkedRecvIsWokenByTheDoorbell pins park/wake: an owner
// already blocked in agent_recv is completed by the arrival, not by a timer.
// The coordinator runs the PRODUCTION sweep cadence, so a receive satisfied
// within the test budget can only have been woken.
func TestSpoolOwner_ParkedRecvIsWokenByTheDoorbell(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, home := awaitCutoverChildIdle(t, c, sp, "first task")
	// Drain what the child's first turn already reported, so the receive
	// below has nothing to return and genuinely parks.
	for {
		if _, err := c.AgentRecv(context.Background(), ownerIdentity(), 20*time.Millisecond); err != nil {
			require.ErrorIs(t, err, ErrRecvTimeout)
			break
		}
	}

	type recvOut struct {
		msgs []Message
		err  error
	}
	parked := make(chan recvOut, 1)
	go func() {
		msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), conformanceWait)
		parked <- recvOut{msgs, err}
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		p := c.polls[ownerIdentity().Harp]
		return p != nil && !p.done
	}, conformanceWait, 5*time.Millisecond, "the owner never parked")

	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "wake up", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	select {
	case r := <-parked:
		require.NoError(t, r.err)
		found := false
		for _, m := range r.msgs {
			if m.Body == "wake up" && m.From == out.Harp {
				found = true
			}
		}
		require.True(t, found, "the parked receive must return the arrival that woke it, got %+v", r.msgs)
	case <-time.After(conformanceWait):
		t.Fatal("the parked owner receive was never woken by the child's send")
	}
}

// TestSpoolOwner_UnackedMailSurvivesRelaunch pins the durable half of
// at-least-once: mail delivered but not acked before the coordinator went
// down is delivered AGAIN by the next coordinator, under the same id.
func TestSpoolOwner_UnackedMailSurvivesRelaunch(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	stateDir := t.TempDir()
	owner := ownerIdentity().Harp

	w, err := spool.NewWriter(spool.NewHomeMapper(), owner, spool.DirIn, spoolWriterIDCoordinator)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{
		Kind: KindReport, FromHarp: "some-child", To: owner,
		OriginID: "m-durable", Body: "written while the owner was down",
	})
	require.NoError(t, err)

	first, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil),
		OwnerHarp: owner,
	})
	require.NoError(t, err)
	require.NoError(t, first.Serve())
	got := recvBody(t, first, "written while the owner was down", conformanceWait)
	require.NotEmpty(t, got, "a cold coordinator must find what is already in the owner's in/")
	assert.Equal(t, "m-durable", got[0].ID)
	first.Close() // delivered, never acked

	second, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil),
		OwnerHarp: owner,
	})
	require.NoError(t, err)
	require.NoError(t, second.Serve())
	t.Cleanup(second.Close)
	again := recvBody(t, second, "written while the owner was down", conformanceWait)
	require.NotEmpty(t, again, "an unacked delivery must be re-delivered after relaunch")
	assert.Equal(t, "m-durable", again[0].ID, "re-delivery keeps the id, which is what lets the reader dedupe")
	recvNothing(t, second, "written while the owner was down")
	_, consumed := spoolEntryWithBody(t, owner, spool.DirInConsumed, "written while the owner was down")
	assert.True(t, consumed)
}

// TestSpoolOwner_RefusesAnUndeclaredOwner pins the fail-loud half of the
// declaration: a coordinator that does not know whose inbox it drains would
// write every child->parent message for nobody.
func TestSpoolOwner_RefusesAnUndeclaredOwner(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	_, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: t.TempDir(), Spawner: newFakeSpawner(nil, nil),
	})
	require.ErrorIs(t, err, ErrNeedsOwner)
}

// TestSpoolOwner_MailToAQueuedChildIsNotStranded reproduces the PRE-LAUNCH
// window: a child is registered at enqueue but its recipient class used to be
// decided only at launch. Mail sent while it waited on the execution cap was
// journaled into the mailbox; after launch the standup drain asked the SPOOL
// how much was pending (zero) and the message was never delivered.
func TestSpoolOwner_MailToAQueuedChildIsNotStranded(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	gate := make(chan struct{})
	sp := cutoverSpawner(0)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{turnGate: gate} }
	c, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: t.TempDir(), Spawner: sp,
		OwnerHarp: ownerIdentity().Harp, ConcurrencyCap: 1,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	// Child A holds the only slot mid-turn; child B queues behind the cap.
	a, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task A", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateExecuting }, conformanceWait, 10*time.Millisecond)
	b, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task B", "", "")
	require.NoError(t, err)
	require.True(t, b.Queued, "child B must be parked on the execution cap for this to be the pre-launch window")
	require.Equal(t, StateQueued, rosterState(c, b.Harp))

	// The owner writes to the QUEUED child.
	_, err = c.AgentSend(ownerIdentity(), b.Harp, KindMessage, "early", nil, "")
	require.NoError(t, err)
	assert.Empty(t, mailboxEverQueued(c), "a migrated child's mail must be a file from the moment it is enqueued, never a mailbox fact")
	assert.Equal(t, 1, c.pendingCount(b.Harp), "the message must be counted as pending for B before it launches")

	// A finishes at its turn boundary and frees the slot; B launches.
	c.recordSummary(a.Harp, a.RunID, 1, finalSummary("FINAL: A done"))
	close(gate)
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)

	awaitChatText(t, sp, 1, "task B")
	awaitChatText(t, sp, 1, "early")
}
