package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// failOpenFs fails Open for exactly one path with a non-NotExist error
// (os.ErrPermission), passing everything else through to the wrapped Fs — the
// seam this regression test uses to distinguish "the ledger doesn't
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

// TestMCPFileConfig_WriteServers_CommandOverride pins the fix at the
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

// TestMCPFileConfig_WriteServers_RefusesUnparsableRegistry pins the fix: an
// unparsable MCP registry used to be warned about and silently degraded to an
// EMPTY table, which every caller (WriteServers) then wrote straight back —
// destroying every user-authored server AND every foreign top-level field on
// a success path. It must now refuse instead, matching the "refuse to
// overwrite, never self-heal" posture corrupt-config handling already uses.
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

// TestMCPFileConfig_WriteServers_LedgerReadErrorSurfaces pins the fix:
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

// TestMCPFileConfig_WriteServers_DedupesManagedNames pins the fix: a server
// name that shadows an earlier one — here, the same name present in both
// bundleMCP and mcp.Servers — used to be appended to `managed` on EVERY add,
// so the ledger persisted duplicate lines for one server.
func TestMCPFileConfig_WriteServers_DedupesManagedNames(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}}

	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{"dup": {Command: "second"}}}
	bundleMCP := map[string]wire.MCPServer{"dup": {Command: "first"}}
	require.NoError(t, c.WriteServers(mcp, bundleMCP))

	ledger, err := afero.ReadFile(fs, "/proj/.mcp-ledger")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(ledger)), "\n")
	count := 0
	for _, l := range lines {
		if l == "dup" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a name shadowed by a later source must appear exactly once in the ledger, not once per source")
}

// TestMCPFileConfig_WriteServers_PreservesLargeNumbers pins that the registry
// rewrite is value-preserving: a number a user put in mcp.json — beside the
// servers block or inside an unmanaged server's own config — must come back
// out of the rewrite as the same literal. A generic decode on the way to the
// canonicaliser rounds anything past float64's exact range, which rewrites the
// user's file while reporting success.
func TestMCPFileConfig_WriteServers_PreservesLargeNumbers(t *testing.T) {
	fs := afero.NewMemMapFs()
	original := `{"timeoutMs": 1234567890123456789,` +
		`"mcpServers": {"theirs": {"command": "x", "budget": 9223372036854775807}}}`
	require.NoError(t, afero.WriteFile(fs, "/proj/mcp.json", []byte(original), 0644))
	c := MCPFileConfig{FS: fs, Path: "/proj/mcp.json", LedgerPath: "/proj/.mcp-ledger", Label: "mcp.json", Warn: func(string, ...interface{}) {}}

	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{"ours": {Command: "ctxloom"}}}
	require.NoError(t, c.WriteServers(mcp, nil))

	data, err := afero.ReadFile(fs, "/proj/mcp.json")
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "1234567890123456789", "a preserved top-level number must survive the rewrite exactly")
	assert.Contains(t, got, "9223372036854775807", "a number inside a preserved server's config must survive too")
	assert.Contains(t, got, `"ours"`, "the managed server is still registered")
}
