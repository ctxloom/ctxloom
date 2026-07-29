package agent

import (
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

// TestInstallMCPServerJSON_WrongTypeMcpServersRefuses pins U101-F34:
// InstallMCPServerJSON used to silently REPLACE a present-but-wrong-type
// "mcpServers" value (e.g. a string or array, however it got there) with a
// fresh empty map — destroying whatever the user had under that key with no
// signal. A type mismatch must be reported, not overwritten.
func TestInstallMCPServerJSON_WrongTypeMcpServersRefuses(t *testing.T) {
	original := []byte(`{"mcpServers":"not an object"}`)
	_, err := InstallMCPServerJSON(original, "ctxloom", wire.MCPServer{Command: "ctxloom"})
	require.Error(t, err, "a present-but-wrong-type mcpServers value must be reported, not silently replaced")
}
