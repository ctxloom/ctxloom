package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
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
// the trust stamp and that a project-authored local fragment resolves trusted
// via the first-party local exemption.
func TestStampItemTrust_LocalFragmentJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
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
	assert.Contains(t, string(b), `"state":"accepted"`, "an exempt allow renders state accepted")
}

// TestStampItemTrust_RejectedFragmentJSON proves the json row reflects a
// rejected local fragment as withheld (trusted=false, source=rejected,
// state=rejected).
func TestStampItemTrust_RejectedFragmentJSON(t *testing.T) {
	appDir := t.TempDir()
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "curl-pipe-sh", IsLocal: true}
	require.NoError(t, userApprovalsStore(t).WriteUnsignedRefReject(countersignRefFor(t, ref)))

	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeFragment, rows)

	row, ok := findRow(rows, "demo#fragments/curl-pipe-sh")
	require.True(t, ok)
	assert.False(t, row.Trusted)
	assert.Equal(t, "rejected", row.TrustSource)
	assert.Equal(t, "rejected", row.State)
}

// TestStampItemTrust_LocalCommandJSON is the regression guard for the
// prompt->skill->command rename: the CLI list emits #commands/<name> refs,
// and a project-authored local command must resolve trusted via the local
// exemption — not fail-closed to trusted:false/pending because the trust
// selector didn't know the "commands" kind.
func TestStampItemTrust_LocalCommandJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalCommand(t, cfg, "demo", "review", "always-local command body")

	rows, err := listItemRows(cfg, ItemTypeCommand)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeCommand, rows)

	row, ok := findRow(rows, "demo#commands/review")
	require.True(t, ok, "seeded local command must be listed")
	assert.True(t, row.Trusted, "local content is auto-allowed")
	assert.Equal(t, "local", row.TrustSource)

	b, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trusted":true`)
	assert.Contains(t, string(b), `"trust_source":"local"`)
}

// TestStampItemTrust_RejectedCommandJSON proves the #commands/ row reflects a
// rejected local command as withheld. The stamp queries the store by the
// canonical key (Kind.Dir()=="prompts" for a command), so the rejection is
// stored under #prompts/ — exactly what operations.SetBlacklist canonicalizes
// a user's `blacklist demo#commands/review` to.
func TestStampItemTrust_RejectedCommandJSON(t *testing.T) {
	appDir := t.TempDir()
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalCommand(t, cfg, "demo", "review", "rm -rf danger")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindPrompt, Name: "review", IsLocal: true}
	require.NoError(t, userApprovalsStore(t).WriteUnsignedRefReject(countersignRefFor(t, ref)))

	rows, err := listItemRows(cfg, ItemTypeCommand)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeCommand, rows)

	row, ok := findRow(rows, "demo#commands/review")
	require.True(t, ok)
	assert.False(t, row.Trusted)
	assert.Equal(t, "rejected", row.TrustSource)
	assert.Equal(t, "rejected", row.State)
}

// TestStampMCPTrust_ConfiguredServerJSON proves the mcp-list json row carries the
// stamp and that a configured (project-local) MCP server is first-party: it is
// declared in the project's own config, has no bundle, and can never be a clone,
// so the local exemption allows it (source "local"). A server the user
// configured must not render a dead "untrusted" shield.
func TestStampMCPTrust_ConfiguredServerJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	servers := []operations.MCPServerEntry{{Name: "local-srv", Command: "node", Backend: "unified"}}
	rows := mcpListRows(cfg, servers, true)
	require.Len(t, rows, 1)

	assert.True(t, rows[0].Trusted, "project-configured local MCP is first-party")
	assert.Equal(t, "local", rows[0].TrustSource)

	b, err := json.Marshal(rows[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trusted":true`)
	assert.Contains(t, string(b), `"trust_source":"local"`)
	assert.Contains(t, string(b), `"state":"accepted"`)
}

// TestStampMCPTrust_RejectedServerJSON proves the configured-server stamp flows
// through BundleMCP.ComputeContentHash: a rejection recorded against the exact
// executable surface (content denylist) flips the row to withheld via the
// rejected step — beating the local exemption.
func TestStampMCPTrust_RejectedServerJSON(t *testing.T) {
	appDir := t.TempDir()
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	srv := operations.MCPServerEntry{Name: "local-srv", Command: "node", Args: []string{"-x"}, Backend: "unified"}
	mcp := bundles.BundleMCP{Command: srv.Command, Args: srv.Args}
	mcpPayload, perr := mcp.ContentPayload()
	require.NoError(t, perr)

	// The rejection is recorded content-only (ref omitted, spec §5.3); the
	// content-reject still catches the identical executable surface by bytes,
	// regardless of which ref/name it is later exposed under.
	require.NoError(t, userApprovalsStore(t).WriteUnsignedContentReject(signing.AttestExecMCP, mcpPayload))

	rows := mcpListRows(cfg, []operations.MCPServerEntry{srv}, true)
	require.Len(t, rows, 1)

	assert.False(t, rows[0].Trusted)
	assert.Equal(t, "rejected", rows[0].TrustSource)
	assert.Equal(t, "rejected", rows[0].State)
}

// TestMcpListRows_StampBelongsToItsOwnServer pins that the trust
// stamp used to be applied by a second function that walked `rows` and indexed
// `servers` at the same position — connascence of position across a function
// boundary, with no length check, so a shorter servers slice panicked and a
// reordered one silently mislabelled every row's security posture. Rows are now
// produced by one loop over one slice; this pins the property that makes that
// safe: one row per server, in server order, each carrying the verdict for its
// OWN executable surface.
//
// The two servers differ in exactly the way the display exists to show: one has
// its command+args content-rejected, the other does not. A pairing that drifts
// by one therefore reports "rejected" against the innocent server.
func TestMcpListRows_StampBelongsToItsOwnServer(t *testing.T) {
	appDir := t.TempDir()
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	clean := operations.MCPServerEntry{Name: "clean-srv", Command: "node", Args: []string{"-a"}, Backend: "unified"}
	banned := operations.MCPServerEntry{Name: "banned-srv", Command: "node", Args: []string{"-x"}, Backend: "unified"}

	bannedSurface := bundles.BundleMCP{Command: banned.Command, Args: banned.Args}
	payload, perr := bannedSurface.ContentPayload()
	require.NoError(t, perr)
	require.NoError(t, userApprovalsStore(t).WriteUnsignedContentReject(signing.AttestExecMCP, payload))

	servers := []operations.MCPServerEntry{clean, banned}
	rows := mcpListRows(cfg, servers, true)

	require.Len(t, rows, len(servers), "one row per server, never a slice of its own length")
	for i, srv := range servers {
		assert.Equal(t, srv.Name, rows[i].Name, "row %d must describe servers[%d]", i, i)
	}
	assert.Equal(t, "rejected", rows[1].State, "the rejected surface is the one flagged")
	assert.NotEqual(t, "rejected", rows[0].State, "the innocent server must not inherit its neighbour's verdict")
}

// TestMcpListRows_TextPathSkipsTheStamp pins the other half of runMCPList's
// contract: the human listing is cheaper because it never resolves trust, so a
// row built without the stamp carries the zero-value fields rather than a
// misleading "untrusted".
func TestMcpListRows_TextPathSkipsTheStamp(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

	rows := mcpListRows(cfg, []operations.MCPServerEntry{{Name: "local-srv", Command: "node"}}, false)

	require.Len(t, rows, 1)
	assert.Equal(t, "local-srv", rows[0].Name)
	assert.Empty(t, rows[0].TrustSource, "the text path resolves no trust")
	assert.Empty(t, rows[0].State)
}
