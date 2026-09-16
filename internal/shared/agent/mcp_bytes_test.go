package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// A remote server renders as the spec's type/url/headers entry and never as
// {"command": ""}: an external registrar handing this helper a URL entry must
// get a server the engine can dial, not an invalid empty command.
func TestMCPServerJSONEntry_RemoteServerWritesTypeURLHeaders(t *testing.T) {
	entry, err := MCPServerJSONEntry("remote", wire.MCPServer{
		URL:     "https://mcp.example.com/v1",
		Headers: map[string]string{"Authorization": "Bearer t"},
	})
	require.NoError(t, err)
	assert.Equal(t, "http", entry["type"])
	assert.Equal(t, "https://mcp.example.com/v1", entry["url"])
	assert.Equal(t, map[string]any{"Authorization": "Bearer t"}, entry["headers"])
	assert.NotContains(t, entry, "command")
}

// A stdio entry spells only what it carries: omitempty is honoured through the
// JSON round trip, so no empty "url"/"env" members are invented.
func TestMCPServerJSONEntry_StdioOmitsEmptyMembers(t *testing.T) {
	entry, err := MCPServerJSONEntry("taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	assert.Equal(t, "taskloom", entry["command"])
	assert.Equal(t, []any{"mcp"}, entry["args"])
	assert.NotContains(t, entry, "url")
	assert.NotContains(t, entry, "env")
}

// A wire entry that is neither stdio nor remote is refused by name, not
// rendered as an empty command.
func TestMCPServerJSONEntry_NoTargetRefused(t *testing.T) {
	_, err := MCPServerJSONEntry("broken", wire.MCPServer{Args: []string{"a"}})
	require.ErrorIs(t, err, wire.ErrMCPServerNoTarget)
	assert.Contains(t, err.Error(), "broken")
}

func TestMCPServerInstalledJSON(t *testing.T) {
	ok, err := MCPServerInstalledJSON([]byte(`{"mcpServers":{"taskloom":{"command":"taskloom"}}}`), "taskloom")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = MCPServerInstalledJSON([]byte(`{"mcpServers":{}}`), "taskloom")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = MCPServerInstalledJSON(nil, "taskloom")
	require.NoError(t, err)
	assert.False(t, ok, "an empty document holds nothing")

	_, err = MCPServerInstalledJSON([]byte(`{not json`), "taskloom")
	require.Error(t, err, "an unparseable document is an error, not 'not installed'")
}
