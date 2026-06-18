package cmd

import "testing"

// Shared MCP wire-protocol test helpers. The mcpClient type and its send/recv
// plumbing live in mcp_bundle_integration_test.go; these drive a session over it.

// initSession performs the initialize / notifications/initialized handshake.
func initSession(t *testing.T, c *mcpClient) {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	_ = c.recvByID(t, 1)
	c.send(t, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
}
