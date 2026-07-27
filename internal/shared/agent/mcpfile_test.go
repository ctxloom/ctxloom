package agent

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCPFileConfig_WriteServers_CommandOverride pins dire-five's fix at the
// shared MCP-registry reconciler kiro and antigravity both bind (mcpFile()):
// a zero-value CommandOverride ("" — every cell but an isolated container)
// writes EXACTLY CtxloomCommand()'s host self-exec-absolute path, byte-for-
// byte unchanged from before the override existed; a non-empty
// CommandOverride (populated ONLY on the container axis) replaces it.
func TestMCPFileConfig_WriteServers_CommandOverride(t *testing.T) {
	t.Run("host-unchanged: empty override writes CtxloomCommand()", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
		require.NoError(t, c.WriteServers(nil, nil))

		data, err := afero.ReadFile(fs, "/proj/mcp.json")
		require.NoError(t, err)
		assert.Contains(t, string(data), CtxloomCommand(), "no override → the host self-exec-absolute command must be unchanged")
	})

	t.Run("container cell: override wins", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		const containerBin = "/usr/local/bin/ctxloom"
		c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}, CommandOverride: containerBin}
		require.NoError(t, c.WriteServers(nil, nil))

		data, err := afero.ReadFile(fs, "/proj/mcp.json")
		require.NoError(t, err)
		assert.Contains(t, string(data), containerBin, "a container-cell override must be the emitted command")
		assert.NotContains(t, string(data), CtxloomCommand(), "the host self-exec path must NOT leak in once an override is set")
	})
}

// TestMCPFileConfig_WriteServers_RefusesUnparsableRegistry pins U101-F03: an
// unparsable MCP registry used to be warned about and silently degraded to an
// EMPTY table, which every caller (WriteServers) then wrote straight back —
// destroying every user-authored server AND every foreign top-level field on
// a success path. It must now refuse instead, matching the "refuse to
// overwrite, never self-heal" posture corrupt-config handling already uses
// (U045-F02/F03).
func TestMCPFileConfig_WriteServers_RefusesUnparsableRegistry(t *testing.T) {
	fs := afero.NewMemMapFs()
	original := []byte(`{ this is not valid json`)
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", original, 0644))

	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
	err := c.WriteServers(nil, nil)
	require.Error(t, err, "an unparsable registry must refuse the write, not silently replace it")

	data, readErr := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, readErr)
	assert.Equal(t, original, data, "the unparsable file must survive untouched")
}
