package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// rawRunChannel is a hand-driven client side of a RunChannel bidi stream: the
// test speaks the wire directly (Hello/HelloAck, plane-2 requests, response
// reads) so it can reconnect at will without a Home in the loop.
type rawRunChannel struct {
	conn   *grpc.ClientConn
	cancel context.CancelFunc
	stream grpc.BidiStreamingClient[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]
	frames chan *agentcoordpb.CoordinatorFrame
}

func dialRawRunChannel(t *testing.T, url, token, runID string) *rawRunChannel {
	t.Helper()
	target, err := grpcTarget(url)
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := agentcoordpb.NewCoordinatorServiceClient(conn).RunChannel(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Hello{Hello: &agentcoordpb.Hello{
		RunId: runID, ProtocolVersion: 1, Capabilities: []string{"peer_messaging"},
	}}}))
	first, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, first.GetHelloAck().GetAccepted(), "raw run channel Hello must be accepted")

	r := &rawRunChannel{conn: conn, cancel: cancel, stream: stream, frames: make(chan *agentcoordpb.CoordinatorFrame, 32)}
	go func() {
		for {
			f, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			select {
			case r.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return r
}

// sendPeerSend issues one plane-2 agent_send to the parent under the given
// request_id. Reusing a request_id is the reqTrack idempotency probe: the
// coordinator must dispatch it exactly once however many frames carry it.
func (r *rawRunChannel) sendPeerSend(t *testing.T, reqID, text string) {
	t.Helper()
	require.NoError(t, r.stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Request{Request: &agentcoordpb.AgentRequest{
		RequestId: reqID,
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress,
			Text:   text,
			Kind:   agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		}},
	}}}))
}

// awaitResponse reads frames until a CoordinatorResponse for reqID arrives
// (skipping acks/notices) and returns it.
func (r *rawRunChannel) awaitResponse(t *testing.T, reqID string, wait time.Duration) *agentcoordpb.CoordinatorResponse {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case f := <-r.frames:
			if resp := f.GetResponse(); resp != nil && resp.GetRequestId() == reqID {
				return resp
			}
		case <-deadline:
			t.Fatalf("no response for %q on the live channel within %s", reqID, wait)
			return nil
		}
	}
}

func (r *rawRunChannel) close() {
	r.cancel()
	_ = r.conn.Close()
}
