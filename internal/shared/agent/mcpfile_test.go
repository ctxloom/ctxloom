package agent

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failOpenFs fails Open for exactly one path with a non-NotExist error
// (os.ErrPermission), passing everything else through to the wrapped Fs — the
// seam U101-F09's regression test uses to distinguish "the ledger doesn't
// exist" from "the ledger could not be read".
type failOpenFs struct {
	afero.Fs
	path string
}

func (f failOpenFs) Open(name string) (afero.File, error) {
	if name == f.path {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.Open(name)
}

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

// TestMCPFileConfig_WriteServers_LedgerReadErrorSurfaces pins U101-F09:
// readLedger mapped ANY read error (not just "does not exist") to nil, so a
// permission/IO failure silently defeated the ledger — dropManaged then
// believed there was nothing previously managed to drop, the exact failure
// the ledger's own doc comment says it exists to prevent.
func TestMCPFileConfig_WriteServers_LedgerReadErrorSurfaces(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/proj/mcp.json", []byte(`{"mcpServers":{}}`), 0644))
	require.NoError(t, afero.WriteFile(base, "/proj/.mcp-ledger", []byte("stale-server\n"), 0644))
	fs := failOpenFs{Fs: base, path: "/proj/.mcp-ledger"}

	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
	err := c.WriteServers(nil, nil)
	require.Error(t, err, "a ledger read failure (not simply missing) must surface, not be silently treated as an empty ledger")
}

// TestMCPFileConfig_RemoveServers_RemovesHuskFile pins U101-F24: when nothing
// remains after removing every managed server and the registry file already
// existed, save() used to fall through and write "{}\n" — a husk file —
// instead of removing it, unlike writeLedger's own "remove when empty"
// behaviour for the sidecar it sits next to.
func TestMCPFileConfig_RemoveServers_RemovesHuskFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(`{"mcpServers":{"ctxloom":{"command":"ctxloom"}}}`), 0644))
	require.NoError(t, afero.WriteFile(fs, "/proj/.mcp-ledger", []byte("ctxloom\n"), 0644))

	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}}
	require.NoError(t, c.RemoveServers())

	exists, err := afero.Exists(fs, "/proj/mcp.json")
	require.NoError(t, err)
	assert.False(t, exists, "an mcp.json left with nothing to write must be removed, not left as a {} husk")
}
