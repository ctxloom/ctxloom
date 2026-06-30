package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// findRow returns the listed row with the given ref.
func findRow(rows []itemRow, ref string) (itemRow, bool) {
	for _, r := range rows {
		if r.Ref == ref {
			return r, true
		}
	}
	return itemRow{}, false
}

// TestStampItemTrust_LocalFragmentJSON proves the fragment-list json row carries
// the TR3 stamp and that a project-authored local fragment resolves trusted via
// the local tier.
func TestStampItemTrust_LocalFragmentJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "always-local body")

	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeFragment, rows)

	row, ok := findRow(rows, "demo#fragments/x")
	require.True(t, ok, "seeded local fragment must be listed")
	assert.True(t, row.Trusted, "local content is auto-allowed")
	assert.Equal(t, "local", row.TrustSource)

	b, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trusted":true`)
	assert.Contains(t, string(b), `"trust_source":"local"`)
}

// TestStampItemTrust_BlacklistedFragmentJSON proves the json row reflects a
// blacklisted local fragment as withheld (trusted=false, source=blacklist).
func TestStampItemTrust_BlacklistedFragmentJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	store := loadTrustStore(t, appDir)
	require.NoError(t, store.Blacklist(remote.LocalSource, "demo#fragments/curl-pipe-sh", ""))

	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeFragment, rows)

	row, ok := findRow(rows, "demo#fragments/curl-pipe-sh")
	require.True(t, ok)
	assert.False(t, row.Trusted)
	assert.Equal(t, "blacklist", row.TrustSource)
}

// TestStampMCPTrust_ConfiguredServerJSON proves the mcp-list json row carries
// the stamp and that a configured (local) MCP server is denied by default — a
// local executable is never auto-trusted.
func TestStampMCPTrust_ConfiguredServerJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}

	servers := []operations.MCPServerEntry{{Name: "local-srv", Command: "node", Backend: "unified"}}
	rows := []mcpListRow{{Name: "local-srv", Command: "node", Backend: "unified"}}
	stampMCPTrust(cfg, servers, rows)

	assert.False(t, rows[0].Trusted, "local executables are not auto-trusted")
	assert.Equal(t, "default", rows[0].TrustSource)

	b, err := json.Marshal(rows[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trusted":false`)
	assert.Contains(t, string(b), `"trust_source":"default"`)
}

// TestStampMCPTrust_GrantedServerJSON proves the configured-server stamp flows
// through BundleMCP.ComputeContentHash: an explicit grant on the exact
// executable surface flips the row to trusted via the grant tier.
func TestStampMCPTrust_GrantedServerJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}

	srv := operations.MCPServerEntry{Name: "local-srv", Command: "node", Args: []string{"-x"}, Backend: "unified"}
	mcp := bundles.BundleMCP{Command: srv.Command, Args: srv.Args}

	store, err := trust.New(paths.TrustPath(appDir))
	require.NoError(t, err)
	require.NoError(t, store.AddGrant(remote.LocalSource, "#mcp/local-srv", mcp.ComputeContentHash(), "raw", ""))

	rows := []mcpListRow{{Name: srv.Name, Command: srv.Command, Args: srv.Args, Backend: srv.Backend}}
	stampMCPTrust(cfg, []operations.MCPServerEntry{srv}, rows)

	assert.True(t, rows[0].Trusted)
	assert.Equal(t, "explicit-grant", rows[0].TrustSource)
}
