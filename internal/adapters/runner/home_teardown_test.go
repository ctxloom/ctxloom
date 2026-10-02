package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// TestHome_ConsumeAfterCrashTouchesNoSpool FORCES the interleaving that made
// a test's temp HOME "not empty" at teardown: a delivery ack (which mkdirs
// the delivered record) reaching the Home AFTER Crash tore it down. A crashed Home
// owns no spool any more — the file stays where it was, and no directory is
// created under a root the test is about to remove.
func TestHome_ConsumeAfterCrashTouchesNoSpool(t *testing.T) {
	teeHome(t)
	const harp = "crashed-harp"
	writeSpoolMail(t, harp, "sender", coord.KindMessage, "left behind")
	entries := spoolEntries(t, harp, spool.DirIn)
	require.Len(t, entries, 1)

	h, err := NewHome(context.Background(), HomeConfig{Reporter: termSink(), URL: "http://127.0.0.1:1/mcp", Token: "t", Harness: "mock", Version: "test", Harp: harp})
	require.NoError(t, err)
	h.mu.Lock()
	h.spoolRefs["m-1"] = entries[0].Ref
	h.mu.Unlock()

	h.Crash()
	h.ackMailConsumed([]string{"m-1"})
	h.sweepSpoolIn()

	assert.Len(t, spoolEntries(t, harp, spool.DirIn), 1, "a crashed Home consumes nothing")
	ids, err := spool.DeliveredIdentities(spool.NewHomeMapper(), harp)
	require.NoError(t, err)
	assert.Empty(t, ids, "nothing is recorded as delivered by a crashed Home")
}

// TestHome_SendBeforeBindIsRefused: a runner cannot send before it knows who
// it is. In production the engine that would call agent_send is started by
// the drive that binds the identity, so the ordering holds by construction;
// a send that arrives anyway is refused by name (ErrIdentityUnbound), never
// answered with the spool writer's own "writer id is required".
func TestHome_SendBeforeBindIsRefused(t *testing.T) {
	teeHome(t)
	h, err := NewHome(context.Background(), HomeConfig{Reporter: termSink(), URL: "http://127.0.0.1:1/mcp", Token: "t", RunID: "run-1", Harness: "mock", Version: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })

	resp, handled := h.sendPeerViaSpool(&agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
		ToRole: coord.ParentAddress, Text: "too early", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
	}}})
	require.True(t, handled)
	assert.EqualValues(t, codes.FailedPrecondition, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Contains(t, resp.GetStatus().GetMessage(), ErrIdentityUnbound.Error())
}
