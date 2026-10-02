package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// Tests for the OWNER HOP of the mail-plane cutover: the session owner is a
// spool recipient, and its in/ is read by its turn-start hook (ownerMail
// reads it the same way).
//
// The owner is the highest-traffic recipient on the bus — every child's FINAL
// report lands there — so every test below asserts the CONTENT arrived, never
// that a call returned true.

// recvNothing performs one bounded owner read and asserts it returned no
// message whose body is body.
func recvNothing(t *testing.T, c *Coordinator, body string) {
	t.Helper()
	msgs, err := ownerMail(t, c, 50*time.Millisecond)
	if err != nil {
		require.ErrorIs(t, err, errNoOwnerMail)
	}
	for _, m := range msgs {
		assert.NotEqual(t, body, m.Body, "a message acked by a prior receive must never be delivered again")
	}
}

// TestSpoolOwner_FinalReportReachesTheOwnerThroughTheSpool is the row's
// settling proof: a child's FINAL report reaches the owner FROM A FILE in the owner's own in/, and no mailbox fact was ever journaled
// for it. Make spoolDeliverTo refuse the owner again and the mailbox-twin
// assertion goes red.
func TestSpoolOwner_FinalReportReachesTheOwnerThroughTheSpool(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	require.NoError(t, c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: the deliverable")))

	got := recvBody(t, c, "FINAL: the deliverable", conformanceWait)
	require.NotEmpty(t, got, "the child's FINAL report must reach the owner")
	assert.Equal(t, KindReport, got[0].Kind)
	assert.Equal(t, out.Harp, got[0].From, "the notice is authored by the child that filed it")

	// THE FILE IS THE MESSAGE: it is in the owner's in/consumed/ (delivered
	// and acknowledged) and nowhere in the mailbox fold.
	entry, ok := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirInConsumed, "FINAL: the deliverable")
	require.True(t, ok, "the delivered report must be the file the owner's reader acknowledged")
	assert.Equal(t, got[0].ID, entry.Message.OriginID, "the mailbox id the owner saw is the file's origin id")
	assertNoMailboxJournal(t, c)
}

// TestSpoolOwner_ChildSendRidesTheFileToTheOwner pins the ordinary
// child->parent send end to end over files: out/ of the child, routed by the
// coordinator into in/ of the owner, read by the owner's reader.
func TestSpoolOwner_ChildSendRidesTheFileToTheOwner(t *testing.T) {
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
	_, inOwnerSpool := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirInConsumed, "a finding")
	assert.True(t, inOwnerSpool, "the routed message must be a file in the owner's spool, acknowledged by its reader")
	assertNoMailboxJournal(t, c)
}

// TestSpoolOwner_ClaimHoldsUntilAck pins at-least-once for the owner: a
// claimed file sits in in/claimed/ — taken, not acknowledged — so nothing
// counts it as waiting in in/, until its ack renames it into in/consumed/;
// and it is never delivered twice.
func TestSpoolOwner_ClaimHoldsUntilAck(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	owner := ownerIdentity().Harp

	require.NoError(t, c.recordSummary(out.Harp, out.RunID, 1, finalSummary("FINAL: once")))
	var entry spool.Entry
	require.Eventually(t, func() bool {
		res, err := spool.Claim(c.mapper, owner)
		if err != nil {
			return false
		}
		for _, e := range res.Entries {
			if e.Message.Body == "FINAL: once" {
				entry = e
				return true
			}
		}
		return false
	}, conformanceWait, time.Millisecond, "the child's FINAL report never reached the owner's spool")

	_, stillIn := spoolEntryWithBody(t, owner, spool.DirIn, "FINAL: once")
	require.False(t, stillIn, "claimed: the file has left in/, so no peek counts it as waiting")
	_, claimed := spoolEntryWithBody(t, owner, spool.ClaimedDirName, "FINAL: once")
	require.True(t, claimed, "claimed but unacked: the file must be in in/claimed/ — the reservation is on disk")

	require.NoError(t, spool.Ack(c.mapper, owner, entry.Ref.Name))
	_, consumed := spoolEntryWithBody(t, owner, spool.DirInConsumed, "FINAL: once")
	assert.True(t, consumed, "the ack is the consume-rename into in/consumed/")
	_, claimed = spoolEntryWithBody(t, owner, spool.ClaimedDirName, "FINAL: once")
	assert.False(t, claimed, "an acked file must have left in/claimed/")
	recvNothing(t, c, "FINAL: once")
}

// TestSpoolOwner_UnackedMailSurvivesRelaunch pins the durable half of
// at-least-once: mail claimed but not acked before the coordinator went down
// is delivered AGAIN from the same spool, under the same id.
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

	teeHome(t)
	first, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil),
		OwnerHarp: owner,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(first))
	claimed, err := spool.Claim(first.mapper, owner)
	require.NoError(t, err)
	require.Len(t, claimed.Entries, 1, "what is already in the owner's in/ must be claimable under a cold coordinator")
	assert.Equal(t, "m-durable", claimed.Entries[0].Message.OriginID)
	first.Close() // claimed, never acked

	teeHome(t)
	second, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: newFakeSpawner(nil, nil),
		OwnerHarp: owner,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(second))
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
	sp.nextChat = func() *scriptedChat { return &scriptedChat{Gate: gate} }
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(), StateDir: t.TempDir(), Spawner: sp,
		OwnerHarp: ownerIdentity().Harp, ConcurrencyCap: 1,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
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
	assertNoMailboxJournal(t, c)
	assert.Equal(t, 1, c.pendingCount(b.Harp), "the message must be counted as pending for B before it launches")

	// A finishes at its turn boundary and frees the slot; B launches.
	require.NoError(t, c.recordSummary(a.Harp, a.RunID, 1, finalSummary("FINAL: A done")))
	close(gate)
	require.Eventually(t, func() bool { return rosterState(c, a.Harp) == StateEnded }, conformanceWait, 10*time.Millisecond)

	awaitChatText(t, sp, 1, "task B")
	awaitChatText(t, sp, 1, "early")
}

// TestSpoolOwner_TheOwnersRunnerSendReachesTheChild pins the OWNER->child
// half over the owner's OWN RUNNER: a `ctxloom run` session's engine reaches
// the coordinator through its runner (the plugin-hosted owner arm), whose
// agent_send is a file in the OWNER's out/ — no coordinator round trip. The
// coordinator must route that file under the owner's identity exactly as it
// routes a child's, or the owner's every send is a message written to a
// directory nothing reads, with DELIVERY_QUEUED reported back.
//
// Asserted on the child's delivered turn and on the owner's out/ being routed
// (consumed), never on the send's own status.
func TestSpoolOwner_TheOwnersRunnerSendReachesTheChild(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")

	url, err := c.ReachURL("host")
	require.NoError(t, err)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	// The owner's runner: no RunID (no launch minted it), its harp from its
	// config — the plugin-hosted arm's shape.
	ownerHome, err := runnerHooks.NewHome(ctx, TestHomeConfig{
		Reporter: termSink(), URL: url, Token: token, Harness: "test", Version: "test",
		Harp: ownerIdentity().Harp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { ownerHome.Close(0, "") })

	const body = "a steer from the owner, via its runner"
	resp, err := ownerHome.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToAgentId: out.Harp, Text: body, Kind: agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	// THE PAYLOAD: the child's engine is handed the owner's words as a turn,
	// authored by the owner — the identity of the spool it was found in.
	var delivered string
	for _, text := range awaitChatText(t, sp, 0, body) {
		if strings.Contains(text, body) {
			delivered = text
		}
	}
	assert.Contains(t, delivered, "from="+ownerIdentity().Harp, "the message is authored by the owner")

	// AND THE ROUTE: the owner's out/ file was routed and consumed, not left
	// in place for a sweep that never comes. Deliver-then-consume is the
	// coordinator's ordering (routeSpoolOut), so the child can see the
	// message a moment before the rename lands.
	require.Eventually(t, func() bool {
		_, pending := spoolEntryWithBody(t, ownerIdentity().Harp, spool.DirOut, body)
		return !pending
	}, conformanceWait, 20*time.Millisecond, "the routed message must not remain in the owner's out/")
	assertNoMailboxJournal(t, c)
}
