package claude

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// .mcp.json is a file ctxloom does not own. Registering a server into it must
// leave everything else exactly as the user wrote it — a typed round trip of
// the whole file would drop every key it does not model on a success path
// with a success message, the shape this project calls its characteristic
// defect, applied to a file the user authored.
//
// Both cases below are real: $schema is conventional in MCP registries, and a
// hand-authored remote entry may carry keys beyond the type/url/headers
// ctxloom itself writes.
func TestWriteMCPConfig_PreservesForeignTopLevelKeys(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	path := filepath.Join(dir, MCPFileName)
	original := `{
  "$schema": "https://example.com/mcp.schema.json",
  "mcpServers": {}
}`
	testsupport.WriteFileString(t, fs, path, original, 0o644)

	w := &ClaudeCodeHookWriter{FS: fs}
	require.NoError(t, w.writeMCPConfig(dir, nil))

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))

	assert.Contains(t, got, "$schema",
		"a top-level key ctxloom does not model must survive; .mcp.json is the user's file")
}

func TestWriteMCPConfig_PreservesUnmodelledServerFields(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	path := filepath.Join(dir, MCPFileName)
	// A REMOTE MCP server ctxloom did not create. It must not be touched.
	original := `{
  "mcpServers": {
    "remote-thing": {
      "type": "http",
      "url": "https://mcp.example.com/v1",
      "headers": {"Authorization": "Bearer abc123"}
    }
  }
}`
	testsupport.WriteFileString(t, fs, path, original, 0o644)

	w := &ClaudeCodeHookWriter{FS: fs}
	require.NoError(t, w.writeMCPConfig(dir, nil))

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))

	servers, ok := got["mcpServers"].(map[string]any)
	require.True(t, ok, "mcpServers must still be an object")
	remote, ok := servers["remote-thing"].(map[string]any)
	require.True(t, ok, "a hand-authored server entry must survive at all")

	assert.Equal(t, "http", remote["type"], "the server's type must survive")
	assert.Equal(t, "https://mcp.example.com/v1", remote["url"], "the server's url must survive")
	assert.Contains(t, remote, "headers", "the server's headers must survive")
	assert.NotContains(t, remote, "command",
		"ctxloom must not invent a command field on a remote server it does not manage")
}

// TestWriteMCPConfig_RemoteServerEntry: a URL-bearing managed server lands
// in .mcp.json as the spec's type/url/headers entry — the engine-native
// `type` derived from the URL at write time — and never as {"command": ""}.
// The stdio entry beside it is unaffected.
func TestWriteMCPConfig_RemoteServerEntry(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	path := filepath.Join(dir, MCPFileName)

	w := &ClaudeCodeHookWriter{FS: fs}
	require.NoError(t, w.writeMCPConfig(dir, map[string]wire.MCPServer{
		"remote": {URL: "https://mcp.example.com/v1", Headers: map[string]string{"Authorization": "Bearer t"}},
		"stdio":  {Command: "cmd", Args: []string{"a"}},
	}))

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(out, &got))

	remote := got.MCPServers["remote"]
	require.NotNil(t, remote, "the remote server must be written at all")
	assert.Equal(t, "http", remote["type"])
	assert.Equal(t, "https://mcp.example.com/v1", remote["url"])
	assert.Equal(t, map[string]any{"Authorization": "Bearer t"}, remote["headers"])
	assert.NotContains(t, remote, "command", "a remote server has no command; an empty one is an invalid entry")

	stdio := got.MCPServers["stdio"]
	require.NotNil(t, stdio)
	assert.Equal(t, "cmd", stdio["command"])
	assert.NotContains(t, stdio, "type")
	assert.NotContains(t, stdio, "url")
}
