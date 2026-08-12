package mcp

import (
	"context"
	"io"
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
	endpoint, err := ServeRunnerMCP(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.SocketPath)
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

// TestBuildForwardServer_RefusesAZeroToolRunner pins that runMCPForward's
// own doc claims "an unreachable runner socket is a hard startup error: a
// silently-empty toolset would be a wrong-context session" — but nothing
// ever checked that ANY tool actually got registered once the connection
// itself succeeded. A runner that connects fine but advertises zero tools
// (a plausible wrong-context shape: e.g. a leaf session's surface computed
// against the wrong routing table) used to stand up a perfectly
// healthy-looking, entirely empty forward server.
func TestBuildForwardServer_RefusesAZeroToolRunner(t *testing.T) {
	empty := mcp.NewServer(&mcp.Implementation{Name: "empty-runner", Version: "test"}, nil)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := empty.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	_, err = buildForwardServer(context.Background(), cs)
	require.Error(t, err, "a runner advertising zero tools must refuse to stand up a forward session, not silently succeed empty")
	assert.Contains(t, err.Error(), "zero tools")
}

// TestDialReachBackSocket_UnixDefault pins the unchanged default: any plain
// path (no tcp:// marker) dials as a unix socket, exactly as before the
// off-Linux fallback existed.
func TestDialReachBackSocket_UnixDefault(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/mcp.sock"
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			_ = conn.Close()
		}
	}()

	conn, err := dialReachBackSocket(context.Background(), sock)
	require.NoError(t, err, "a plain path must still dial as unix")
	_ = conn.Close()
}

// TestDialReachBackSocket_TCPFallback pins the off-Linux fallback's far side:
// a "tcp://host:port" value dials TCP, not unix — the exact form
// internal/acp/container_transport.go's containerReachBackEnv emits on
// darwin/windows.
func TestDialReachBackSocket_TCPFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			_ = conn.Close()
		}
	}()

	conn, err := dialReachBackSocket(context.Background(), "tcp://"+ln.Addr().String())
	require.NoError(t, err, "a tcp:// value must dial TCP")
	_ = conn.Close()
}

// TestForward_TCPFallbackRoundTrip is TestForward_UnixSocketRoundTrip's
// off-Linux twin: the SAME runner MCP endpoint, reached over a TCP bridge
// (standing in for internal/acp/container_transport.go's reachBackBridge)
// instead of the unix socket directly, dialed via dialReachBackSocket exactly
// as runMCPForward would. Proves the far-side dial change is not just wiring
// — the whole forwarded toolset survives the TCP hop.
func TestForward_TCPFallbackRoundTrip(t *testing.T) {
	endpoint, err := ServeRunnerMCP(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	// A minimal TCP<->unix bridge, mirroring reachBackBridge without
	// importing internal/acp (which would cycle back into this package).
	bridgeLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bridgeLn.Close() })
	go func() {
		for {
			conn, aerr := bridgeLn.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				uc, uerr := net.Dial("unix", endpoint.SocketPath)
				if uerr != nil {
					return
				}
				defer func() { _ = uc.Close() }()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(uc, c); close(done) }()
				_, _ = io.Copy(c, uc)
				<-done
			}(conn)
		}
	}()

	tcpAddr := "tcp://" + bridgeLn.Addr().String()

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialReachBackSocket(ctx, tcpAddr)
			},
		}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	server, err := buildForwardServer(context.Background(), cs)
	require.NoError(t, err)

	tools := listServerTools(t, server)
	for name := range mcpschema.Routes() {
		_, ok := tools[name]
		assert.True(t, ok, "forwarded surface (over the TCP fallback) is missing %q", name)
	}
}
