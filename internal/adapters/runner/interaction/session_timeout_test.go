package interaction_test

import (
	"context"
	"slices"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
)

// TestServe_AnAbandonedSessionIsClosedByTheIdleTimeout: a session whose
// client is still alive but never asks anything again — a relay claude
// replaced, the spare session of a re-initialization — is attributable to no
// exit, so the idle timeout collects it. The client here keeps no keepalive.
func TestServe_AnAbandonedSessionIsClosedByTheIdleTimeout(t *testing.T) {
	lo := loadoutAt(freePort(t))
	servers := make(chan *sdk.Server, 1)
	ep := interaction.WithServerHook(interaction.Endpoint{Home: deadHome(t), Wake: interaction.NewWakeSignal(nil), SessionTimeout: 200 * time.Millisecond},
		func(s *sdk.Server) { servers <- s })
	served, err := ep.Serve(context.Background(), lo, delivery.ServePolicy{AllowedOrigins: []string{"http://127.0.0.1"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = served.Close() })
	server := <-servers
	connect(t, lo.MCP.URL, bearer)
	open := slices.Collect(server.Sessions())
	require.Len(t, open, 1)

	closed := make(chan error, 1)
	go func() { closed <- open[0].Wait() }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the idle session was never closed")
	}

	assert.Empty(t, slices.Collect(server.Sessions()), "the abandoned session is gone from the endpoint")
}
