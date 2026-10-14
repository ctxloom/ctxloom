package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestSendPeerViaSpool_SaysWhenTheRecipientWillReadIt is alpha issue 1's other
// half: agent_send answered "written to this session's outbound spool", and a
// parent took that as "the child has it" while its binding reply sat unread
// through the child's whole turn. The answer must say the message is QUEUED
// and when the recipient actually gets it.
func TestSendPeerViaSpool_SaysWhenTheRecipientWillReadIt(t *testing.T) {
	home := boundHome(t, coord.Identity{Harp: "sender-harp", Depth: 1})
	resp, handled := home.sendPeerViaSpool(&agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: "parent", Text: "status", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		}},
	})
	require.True(t, handled)
	require.EqualValues(t, 0, resp.GetStatus().GetCode())
	msg := resp.GetStatus().GetMessage()
	assert.Contains(t, msg, "queued", "the message is queued, not delivered")
	assert.Contains(t, msg, "next tool call", "it must say WHEN the recipient reads it: mid-turn, at a tool call")
	assert.Contains(t, msg, "turn boundary", "…or at the turn's end")
	assert.NotContains(t, msg, "delivered to", "nothing has reached the recipient yet")
	assert.Equal(t, agentcoordpb.PeerSendResult_DELIVERY_QUEUED, resp.GetPeerSend().GetDelivery())
}
