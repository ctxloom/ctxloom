package codex

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestRemoveLedgeredMCPServers_EmptiesTableDeletesKey is the direct unit test
// for removeLedgeredMCPServers's own empty-table cleanup: dropping the LAST
// name it was told to remove must delete the whole "mcp_servers" key, not
// leave an empty stanza behind — the same "no bare empty table" contract
// removeManagedMCP already holds.
//
// MUTATION TARGET: flip `if len(servers) == 0` to `if len(servers) != 0` (or
// drop the block) and this goes red — an empty mcp_servers table survives.
func TestRemoveLedgeredMCPServers_EmptiesTableDeletesKey(t *testing.T) {
	cfg := map[string]any{"mcp_servers": map[string]any{
		"only-one": map[string]any{"command": "third-party-cmd"},
	}}
	removeLedgeredMCPServers(cfg, []string{"only-one"})
	assert.NotContains(t, cfg, "mcp_servers", "emptying the table via the ledger's name list must delete the key, not leave a bare stanza")
}

// TestRemoveLedgeredMCPServers_PartialRemovalKeepsTheRest is
// TestRemoveLedgeredMCPServers_EmptiesTableDeletesKey's converse: removing
// SOME of the ledger's names must leave every other entry — foreign,
// user-authored, or simply not-yet-renamed-away — untouched.
func TestRemoveLedgeredMCPServers_PartialRemovalKeepsTheRest(t *testing.T) {
	cfg := map[string]any{"mcp_servers": map[string]any{
		"old-name": map[string]any{"command": "third-party-cmd"},
		"other":    map[string]any{"command": "unrelated-cmd"},
	}}
	removeLedgeredMCPServers(cfg, []string{"old-name"})
	servers := asMap(cfg["mcp_servers"])
	require.NotNil(t, servers)
	assert.NotContains(t, servers, "old-name")
	assert.Contains(t, servers, "other")
}

// TestWriteSettingsIn_RenamedBundleServerIsWithdrawn is the PAYLOAD test for
// R6/config-patching-review.md caveat C1: codex's [mcp_servers] ownership used
// to be STRUCTURAL ONLY (removeManagedMCP recognizes ctxloom's own well-known
// entry, and anything whose COMMAND resolves to ctxloom) — but a bundle server
// can name any third-party executable, so a server ctxloom declared under one
// name and then renamed left the OLD entry behind forever: nothing recognized
// it as ctxloom's to remove. The SurfaceMCP ledger fixes this by recording the
// name set directly, the same way agent.MCPFileConfig's ledger does for the
// JSON engines.
//
// MUTATION TARGET: drop the `removeLedgeredMCPServers(cfg, prevMCP)` call from
// writeSettingsIn (or its ledger.Write at the end) and this goes red — the
// renamed-away "old-name" entry survives the second write.
func TestWriteSettingsIn_RenamedBundleServerIsWithdrawn(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}

	// First run: a bundle-managed server with a THIRD-PARTY command — the
	// exact shape removeManagedMCP's command-based check cannot recognize.
	bundleMCP := map[string]wire.MCPServer{"old-name": {Command: "third-party-cmd"}}
	require.NoError(t, w.writeSettingsIn(&wire.HooksConfig{}, bundleMCP, "/proj", ""))

	cfg := readConfig(t, fs, codexConfigPath("/proj"))
	servers := asMap(cfg["mcp_servers"])
	require.Contains(t, servers, "old-name", "first write installs the server under its declared name")

	// Second run: the SAME server, renamed. Nothing about "old-name" is
	// declared anymore.
	bundleMCP2 := map[string]wire.MCPServer{"new-name": {Command: "third-party-cmd"}}
	require.NoError(t, w.writeSettingsIn(&wire.HooksConfig{}, bundleMCP2, "/proj", ""))

	cfg2 := readConfig(t, fs, codexConfigPath("/proj"))
	servers2 := asMap(cfg2["mcp_servers"])
	assert.Contains(t, servers2, "new-name", "the renamed server is present under its new name")
	assert.NotContains(t, servers2, "old-name",
		"the OLD name must be withdrawn — a purely structural (command-based) ownership check can never find it, since the command is a third-party one")
}

// TestWriteSettingsIn_WritesSurfaceMCPLedger pins the ledger record itself:
// after a write, ledger.Ledger.Read(SurfaceMCP) names exactly the managed
// servers addMCPServers just wrote — every server the resolved bundles ship,
// ctxloom's own included — so the NEXT reconcile knows precisely what it owns.
func TestWriteSettingsIn_WritesSurfaceMCPLedger(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}

	bundleMCP := map[string]wire.MCPServer{
		agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}},
		"config-server":     {Command: "config-cmd"},
		"bundle-server":     {Command: "bundle-cmd"},
	}
	require.NoError(t, w.writeSettingsIn(&wire.HooksConfig{}, bundleMCP, "/proj", ""))

	led := ledger.Ledger{FS: fs, Dir: cellScopedCodexHome("/proj")}
	names, err := led.Read(ledger.SurfaceMCP)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{agent.MCPServerName, "config-server", "bundle-server"}, names)
}

// TestRemoveSettingsIn_ClearsSurfaceMCPLedger pins the uninstall half: after
// removeSettingsIn, the SurfaceMCP ledger is empty, not just the file's
// [mcp_servers] table — a revert must withdraw the RECORD as completely as
// the bytes, or a later re-install would misjudge what the ledger already
// knew about.
func TestRemoveSettingsIn_ClearsSurfaceMCPLedger(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}

	bundleMCP := map[string]wire.MCPServer{"bundle-server": {Command: "bundle-cmd"}}
	require.NoError(t, w.writeSettingsIn(&wire.HooksConfig{}, bundleMCP, "/proj", ""))

	led := ledger.Ledger{FS: fs, Dir: cellScopedCodexHome("/proj")}
	before, err := led.Read(ledger.SurfaceMCP)
	require.NoError(t, err)
	require.NotEmpty(t, before, "sanity: the ledger recorded something to withdraw")

	require.NoError(t, w.removeSettingsIn("/proj"))

	after, err := led.Read(ledger.SurfaceMCP)
	require.NoError(t, err)
	assert.Empty(t, after, "the ledger record must be withdrawn along with the bytes")
}
