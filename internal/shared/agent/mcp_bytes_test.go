package agent

import (
	"encoding/json"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstallMCPServerJSON_AbsentMcpServersCreatesMap proves the legitimate
// case is unchanged: no "mcpServers" key at all is a fresh install, not an
// error.
func TestInstallMCPServerJSON_AbsentMcpServersCreatesMap(t *testing.T) {
	out, err := InstallMCPServerJSON([]byte(`{"other":"kept"}`), "ctxloom", wire.MCPServer{Command: "ctxloom"})
	require.NoError(t, err)
	assert.Contains(t, string(out), `"mcpServers"`)
	assert.Contains(t, string(out), `"other": "kept"`)
}

// TestInstallMCPServerJSON_WrongTypeMcpServersRefuses pins the fix:
// InstallMCPServerJSON used to silently REPLACE a present-but-wrong-type
// "mcpServers" value (e.g. a string or array, however it got there) with a
// fresh empty map — destroying whatever the user had under that key with no
// signal. A type mismatch must be reported, not overwritten.
func TestInstallMCPServerJSON_WrongTypeMcpServersRefuses(t *testing.T) {
	original := []byte(`{"mcpServers":"not an object"}`)
	_, err := InstallMCPServerJSON(original, "ctxloom", wire.MCPServer{Command: "ctxloom"})
	require.Error(t, err, "a present-but-wrong-type mcpServers value must be reported, not silently replaced")
}

// A remote server registers as the spec's type/url/headers entry and never
// as {"command": ""}: an external registrar handing this helper a URL entry
// must get a server the engine can dial, not an invalid empty command.
func TestInstallMCPServerJSON_RemoteServerWritesTypeURLHeaders(t *testing.T) {
	out, err := InstallMCPServerJSON(nil, "remote", wire.MCPServer{
		URL:     "https://mcp.example.com/v1",
		Headers: map[string]string{"Authorization": "Bearer t"},
	})
	require.NoError(t, err)

	var doc map[string]map[string]map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	entry := doc["mcpServers"]["remote"]
	assert.Equal(t, "http", entry["type"])
	assert.Equal(t, "https://mcp.example.com/v1", entry["url"])
	assert.Equal(t, map[string]any{"Authorization": "Bearer t"}, entry["headers"])
	assert.NotContains(t, entry, "command")
}

// A wire entry that is neither stdio nor remote is refused by name, not
// registered as an empty command.
func TestInstallMCPServerJSON_NoTargetRefused(t *testing.T) {
	_, err := InstallMCPServerJSON(nil, "broken", wire.MCPServer{Args: []string{"a"}})
	require.ErrorIs(t, err, wire.ErrMCPServerNoTarget)
	assert.Contains(t, err.Error(), "broken")
}
