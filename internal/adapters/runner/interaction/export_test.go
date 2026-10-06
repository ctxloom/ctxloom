package interaction

import "github.com/modelcontextprotocol/go-sdk/mcp"

// WithServeGate returns e with gate run at the head of its serve goroutine,
// before http.Server.Serve registers the listener, so a test can hold the
// endpoint in the window where Shutdown alone does not own the listener.
func WithServeGate(e Endpoint, gate func()) Endpoint {
	e.serveGate = gate
	return e
}

// WithReapHook returns e with hook run each time a reap of its sessions has
// closed every one of them, so a test can wait for the closes instead of
// polling for their effect.
func WithReapHook(e Endpoint, hook func()) Endpoint {
	e.reaped = hook
	return e
}

// WithServerHook returns e with hook handed the MCP server each Serve builds,
// so a test can wait on that server's own sessions.
func WithServerHook(e Endpoint, hook func(*mcp.Server)) Endpoint {
	e.served = hook
	return e
}
