package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// TestSpoolCutover_MailRidesTheSpoolWithNothingAsked pins the post-cutover
// contract: the file spool is THE carrier, not a carrier a project opts into.
//
// The coordinator here is built the way every production caller builds one —
// an owner declared, and nothing else said about delivery — and both
// directions must ride files: an owner send to a migrated child lands in the
// child's in/ and is consumed by rename with NO mailbox twin, and the child's
// agent_send lands in its out/ and satisfies the owner's agent_recv. Neither
// end may need telling: the runner learns nothing from a per-spawn stamp
// because there is nothing left to learn.
//
// With a per-run cutover flag still in the tree this is RED, because the
// default it defends is the mailbox — the exact parked state this test
// retires.
func TestSpoolCutover_MailRidesTheSpoolWithNothingAsked(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		OwnerHarp:  ownerIdentity().Harp,
	})
	require.NoError(t, err, "a coordinator with an owner and nothing else said must come up on the spool")
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "first task", "", "")
	require.NoError(t, err)
	upCtx, upCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer upCancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp), "the migrated child never came up")
	require.Eventually(t, func() bool { return sp.engineHome(0) != nil }, conformanceWait, 10*time.Millisecond,
		"the runner half never appeared")
	home := sp.engineHome(0)

	// Down: the owner's send is ONE file in the child's in/, delivered as a
	// turn and consumed by rename. No mailbox fact exists for it.
	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "second task", nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, msgID)
	awaitChatText(t, sp, 0, "second task")
	consumed := awaitSpoolEntryWithBody(t, out.Harp, spool.DirInConsumed, "second task", "after delivery")
	assert.Equal(t, msgID, consumed.Message.OriginID, "the consumed file is the message the owner sent")
	assertNoMailboxJournal(t, c)

	// Up: the child's agent_send is a local file write that the coordinator
	// routes into the owner's agent_recv.
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress,
			Text:   "a finding",
			Kind:   agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the local write must succeed: %s", resp.GetStatus().GetMessage())
	got := recvBody(t, c, "a finding", conformanceWait)
	require.NotEmpty(t, got, "the owner's agent_recv must be satisfied from the child's out/ spool")
	assert.Equal(t, out.Harp, got[0].From)
	awaitSpoolEntryWithBody(t, out.Harp, spool.DirOutConsumed, "a finding", "after routing")
	assertNoMailboxJournal(t, c)
}
