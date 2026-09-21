package coordgrpc

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
)

// bearerOf attaches one credential to every RPC, the way runner.Home dials.
type bearerOf string

func (b bearerOf) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (bearerOf) RequireTransportSecurity() bool { return false }

// TestBridge_RefusesAnUnauthenticatedRunnerFrame is the bridge's posture
// gate: the listener is BEARER-authenticated and cleartext (mTLS is a later
// slice's), so a frame carrying no credential the coordinator issued is
// refused before its Hello is read — UNAUTHENTICATED, never a HelloAck. The
// runner dials this with the credential minted for its run
// (sessions.EncodeReach); a process on the same loopback that guesses the
// port gets exactly this.
func TestBridge_RefusesAnUnauthenticatedRunnerFrame(t *testing.T) {
	c := coordharness.New(t, t.TempDir())
	require.NoError(t, Serve(c))

	u, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	for _, cred := range []string{"", "not-a-credential-the-coordinator-issued"} {
		conn, err := grpc.NewClient(u.Host,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithPerRPCCredentials(bearerOf(cred)))
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stream, err := agentcoordpb.NewCoordinatorServiceClient(conn).RunnerChannel(ctx)
		require.NoError(t, err)
		_ = stream.Send(&agentcoordpb.RunnerFrame{Kind: &agentcoordpb.RunnerFrame_Hello{Hello: &agentcoordpb.RunnerHello{}}})
		_, err = stream.Recv()
		require.Error(t, err, "credential %q: an unauthenticated frame must not be acknowledged", cred)
		require.Equal(t, codes.Unauthenticated, status.Code(err), "credential %q", cred)
		cancel()
		_ = conn.Close()
	}
}
