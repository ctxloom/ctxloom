package coord

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestHome_RedialPreemptsThePendingBackoff pins that Redial makes a Home's
// NEXT dial attempt happen now instead of at the end of its backoff. Forced
// against a server that fails every RPC (each RPC is one dial attempt by one
// of the Home's loops), with a backoff so long that no test ever outlives it:
// once the first attempts have failed and the loops are parked in their
// backoff, any further attempt can only have come from the kick. The
// assertion therefore needs no tight deadline.
//
// Attempts are counted as RPCs, not TCP accepts: grpc's ClientConn
// reconnects a refused transport on its own backoff, so an accept count
// rises with or without a kick and cannot tell the two apart.
func TestHome_RedialPreemptsThePendingBackoff(t *testing.T) {
	const redialBackoff = time.Hour
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var attempts atomic.Int32
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(any, grpc.ServerStream) error {
		attempts.Add(1)
		return status.Error(codes.Unavailable, "refused: the loop goes into its backoff")
	}))
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	h, err := runnerHooks.NewHome(context.Background(), TestHomeConfig{
		Reporter: termSink(),
		URL:      fmt.Sprintf("http://%s/mcp", ln.Addr().String()),
		Token:    "t", RunID: "run-1", Harness: "mock", Version: "test",
		RedialBackoff: redialBackoff,
	})
	require.NoError(t, err)
	t.Cleanup(h.Crash)

	// Both loops (RunnerChannel, RunChannel) have dialed once and been refused.
	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, conformanceWait, 5*time.Millisecond)
	before := attempts.Load()

	h.Redial()
	require.Eventually(t, func() bool { return attempts.Load() > before }, conformanceWait, 5*time.Millisecond,
		"a kicked Home must redial now; without the kick the next attempt is %s away", redialBackoff)
}
