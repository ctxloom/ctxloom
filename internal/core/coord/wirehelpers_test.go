package coord

import (
	"context"
	"net/url"
	"testing"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// serveWire is the plane-2 path a test drives by hand: the wire request
// decoded, dispatched to its verb, and the reply encoded — what the run
// channel handler does per frame, without a stream.
func serveWire(c *Coordinator, caller Identity, req *agentcoordpb.AgentRequest) *agentcoordpb.CoordinatorResponse {
	decoded, err := AgentRequestFromWire(req)
	if err != nil {
		return AgentReplyToWire(AgentReply{RequestID: decoded.RequestID, Err: decodeRefusal(err)})
	}
	reply := c.serveAgentRequest(caller, decoded)
	reply.RequestID = decoded.RequestID
	return AgentReplyToWire(reply)
}

// dialCoordinator opens a gRPC client connection to the coordinator's
// loopback listener under token — the way a runner dials home, for tests that
// need to send what the runner's own API would never construct.
func dialCoordinator(t *testing.T, coordURL, token string) *grpc.ClientConn {
	t.Helper()
	u, err := url.Parse(coordURL)
	require.NoError(t, err)
	conn, err := grpc.NewClient(u.Host,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearer(token)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// bearer attaches the credential to every RPC.
type bearer string

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearer) RequireTransportSecurity() bool { return false }
