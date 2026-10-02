package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatMCPConfigEntryOf_StdioPreservesEnvVerbatim: a stdio server's Env
// map must reach the marshaled entry byte-for-byte — a server's Env is
// what it was configured with, and a dropped key hands the engine a server
// that starts and cannot reach what it was configured for.
func TestChatMCPConfigEntryOf_StdioPreservesEnvVerbatim(t *testing.T) {
	entry, err := ChatMCPConfigEntryOf(ChatMCPServer{
		Name:    "ctxloom",
		Command: "/usr/local/bin/ctxloom",
		Args:    []string{"mcp", "serve"},
		Env:     map[string]string{"EXAMPLE_SOCKET": "/run/sock.sock"},
	})
	require.NoError(t, err)
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.Equal(t, "/usr/local/bin/ctxloom", entry.Command)
	assert.Equal(t, []string{"mcp", "serve"}, entry.Args)
	assert.Equal(t, map[string]string{"EXAMPLE_SOCKET": "/run/sock.sock"}, entry.Env)
	assert.Equal(t, "", entry.Type)

	assert.Contains(t, string(data), `"EXAMPLE_SOCKET":"/run/sock.sock"`)
	assert.Contains(t, string(data), `"command":"/usr/local/bin/ctxloom"`)
}

// TestChatMCPConfigEntryOf_HTTPAndSSE: remote servers carry type/url/headers,
// never command/args/env.
func TestChatMCPConfigEntryOf_HTTPAndSSE(t *testing.T) {
	http, err := ChatMCPConfigEntryOf(ChatMCPServer{Name: "remote-http", Transport: MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}})
	require.NoError(t, err)
	assert.Equal(t, "http", http.Type)
	assert.Equal(t, "https://example.com/mcp", http.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer tok"}, http.Headers)
	assert.Equal(t, "", http.Command)

	sse, err := ChatMCPConfigEntryOf(ChatMCPServer{Name: "remote-sse", Transport: MCPTransportSSE, URL: "https://example.com/sse"})
	require.NoError(t, err)
	assert.Equal(t, "sse", sse.Type)
	assert.Equal(t, "https://example.com/sse", sse.URL)

	for _, e := range []ChatMCPConfigEntry{http, sse} {
		data, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(data), `"command"`)
	}
}

// TestChatMCPConfigEntryOf_UnknownTransport_Refused: an unrecognized
// transport string must be refused loudly, not silently dropped or
// mis-written.
func TestChatMCPConfigEntryOf_UnknownTransport_Refused(t *testing.T) {
	_, err := ChatMCPConfigEntryOf(ChatMCPServer{Name: "bad", Transport: "carrier-pigeon"})
	require.ErrorIs(t, err, ErrChatMCPConfigTransportUnsupported)
}
