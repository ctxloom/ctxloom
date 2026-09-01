package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarshalChatMCPConfig_StdioPreservesEnvVerbatim: a stdio server's Env
// map (carrying the coordinator's CTXLOOM_MCP_SOCKET stamp) must reach the
// marshaled document byte-for-byte — this is the reach-back channel a
// delegated child uses to call back to its parent, so a dropped key here
// breaks delegation silently.
func TestMarshalChatMCPConfig_StdioPreservesEnvVerbatim(t *testing.T) {
	servers := []ChatMCPServer{
		{
			Name:    "ctxloom",
			Command: "/usr/local/bin/ctxloom",
			Args:    []string{"mcp", "serve"},
			Env:     map[string]string{"CTXLOOM_MCP_SOCKET": "/run/sock.sock"},
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
	assert.Equal(t, map[string]string{"CTXLOOM_MCP_SOCKET": "/run/sock.sock"}, entry.Env)
	assert.Equal(t, "", entry.Type)

	assert.Contains(t, string(data), `"CTXLOOM_MCP_SOCKET":"/run/sock.sock"`)
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

// TestWriteChatMCPConfigFile_Mode0600: the written file must carry mode
// 0o600 exactly (this config can carry MCP server auth headers/env) and its
// content must match MarshalChatMCPConfig's own output.
func TestWriteChatMCPConfigFile_Mode0600(t *testing.T) {
	servers := []ChatMCPServer{
		{Name: "ctxloom", Command: "/usr/local/bin/ctxloom", Args: []string{"mcp", "serve"}},
	}
	path := filepath.Join(t.TempDir(), ".mcp.json")

	require.NoError(t, WriteChatMCPConfigFile(path, servers))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	want, err := MarshalChatMCPConfig(servers)
	require.NoError(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestWriteChatMCPConfigFile_UnknownTransport_Refused: the write half
// refuses the same way the marshal half does, and writes no file.
func TestWriteChatMCPConfigFile_UnknownTransport_Refused(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")

	err := WriteChatMCPConfigFile(path, []ChatMCPServer{{Name: "bad", Transport: "carrier-pigeon"}})
	require.ErrorIs(t, err, ErrChatMCPConfigTransportUnsupported)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "no file should be written on a refused transport")
}
