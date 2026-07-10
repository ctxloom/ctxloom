package cli

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// TestForward_UnixSocketRoundTrip drives the shim's forward transport
// end-to-end against a live runner MCP endpoint: serve on a unix socket,
// connect exactly as runMCPForward does, and mirror the toolset — the two
// modes of `ctxloom mcp` (forward-to-runner, bare local) both covered by
// tests (playbook deliverable 1).
func TestForward_UnixSocketRoundTrip(t *testing.T) {
	endpoint, err := serveRunnerMCP(testConfig(), "test-harp", testHome(t))
	require.NoError(t, err)
	t.Cleanup(endpoint.close)

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.socketPath)
			},
		}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	server, err := buildForwardServer(context.Background(), cs)
	require.NoError(t, err)

	// The mirrored stdio surface carries the whole classified toolset.
	tools := listServerTools(t, server)
	for name := range mcpschema.Routes() {
		_, ok := tools[name]
		assert.True(t, ok, "forwarded surface is missing %q", name)
	}
}
