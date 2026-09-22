package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarshalChatMCPConfig_StdioPreservesEnvVerbatim: a stdio server's Env
// map must reach the marshaled document byte-for-byte — a server's Env is
// what it was configured with, and a dropped key hands the engine a server
// that starts and cannot reach what it was configured for.
func TestMarshalChatMCPConfig_StdioPreservesEnvVerbatim(t *testing.T) {
	servers := []ChatMCPServer{
		{
			Name:    "ctxloom",
			Command: "/usr/local/bin/ctxloom",
			Args:    []string{"mcp", "serve"},
			Env:     map[string]string{"EXAMPLE_SOCKET": "/run/sock.sock"},
		},
	}

	data, err := MarshalChatMCPConfig(servers)
	require.NoError(t, err)

	var doc ChatMCPConfigDoc
	require.NoError(t, json.Unmarshal(data, &doc))
	entry, ok := doc.MCPServers["ctxloom"]
	require.True(t, ok)
	assert.Equal(t, "/usr/local/bin/ctxloom", entry.Command)
	assert.Equal(t, []string{"mcp", "serve"}, entry.Args)
	assert.Equal(t, map[string]string{"EXAMPLE_SOCKET": "/run/sock.sock"}, entry.Env)
	assert.Equal(t, "", entry.Type)

	assert.Contains(t, string(data), `"EXAMPLE_SOCKET":"/run/sock.sock"`)
	assert.Contains(t, string(data), `"command":"/usr/local/bin/ctxloom"`)
}

// TestMarshalChatMCPConfig_HTTPAndSSE: remote servers carry type/url/headers,
// never command/args/env.
func TestMarshalChatMCPConfig_HTTPAndSSE(t *testing.T) {
	servers := []ChatMCPServer{
		{Name: "remote-http", Transport: MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
		{Name: "remote-sse", Transport: MCPTransportSSE, URL: "https://example.com/sse"},
	}

	data, err := MarshalChatMCPConfig(servers)
	require.NoError(t, err)

	var doc ChatMCPConfigDoc
	require.NoError(t, json.Unmarshal(data, &doc))

	http, ok := doc.MCPServers["remote-http"]
	require.True(t, ok)
	assert.Equal(t, "http", http.Type)
	assert.Equal(t, "https://example.com/mcp", http.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer tok"}, http.Headers)
	assert.Equal(t, "", http.Command)

	sse, ok := doc.MCPServers["remote-sse"]
	require.True(t, ok)
	assert.Equal(t, "sse", sse.Type)
	assert.Equal(t, "https://example.com/sse", sse.URL)

	s := string(data)
	assert.NotContains(t, s, `"command"`)
}

// TestMarshalChatMCPConfig_UnknownTransport_Refused: an unrecognized
// transport string must be refused loudly, not silently dropped or
// mis-written.
func TestMarshalChatMCPConfig_UnknownTransport_Refused(t *testing.T) {
	_, err := MarshalChatMCPConfig([]ChatMCPServer{{Name: "bad", Transport: "carrier-pigeon"}})
	require.ErrorIs(t, err, ErrChatMCPConfigTransportUnsupported)
}
