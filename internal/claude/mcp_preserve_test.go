package claude

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// .mcp.json is a file ctxloom does not own. Registering a server into it must
// leave everything else exactly as the user wrote it.
//
// It does not today, and these tests characterize how. writeMCPConfig round-
// trips the whole file through claudeCodeMCPConfig, which models ONE field
// (mcpServers) whose values model five (command/args/env/cwd/_ctxloom).
// Anything outside that shape has nowhere to live across the round trip, so it
// is dropped on a success path with a success message — the shape this project
// calls its characteristic defect, applied to a file the user authored.
//
// Both cases below are real: $schema is conventional in MCP registries, and the
// type/url/headers shape is how the MCP spec spells a REMOTE server, which
// ctxloom models nowhere (see taskloom unelected-clutter).
func TestWriteMCPConfig_PreservesForeignTopLevelKeys(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	path := filepath.Join(dir, MCPFileName)
	original := `{
  "$schema": "https://example.com/mcp.schema.json",
  "mcpServers": {}
}`
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0o644))

	w := &ClaudeCodeHookWriter{FS: fs}
	require.NoError(t, w.writeMCPConfig(dir, &wire.MCPConfig{}, nil))

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
	// A REMOTE MCP server: the spec's type/url/headers shape, none of which
	// claudeCodeMCPServer models. ctxloom must not touch an entry it did not
	// create.
	original := `{
  "mcpServers": {
    "remote-thing": {
      "type": "http",
      "url": "https://mcp.example.com/v1",
      "headers": {"Authorization": "Bearer abc123"}
    }
  }
}`
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0o644))

	w := &ClaudeCodeHookWriter{FS: fs}
	require.NoError(t, w.writeMCPConfig(dir, &wire.MCPConfig{}, nil))

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
