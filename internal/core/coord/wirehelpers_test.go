package coord

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

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
