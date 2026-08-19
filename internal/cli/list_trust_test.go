package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
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

// TestMcpListRows_CarriesSourceBundle pins what the mcp-list json row is FOR
// now that every listed server is a bundle item: the row names the bundle the
// server came from, which is also the identity `ctxloom bundle trust|reject
// <bundle>#mcp/<name>` addresses. There is no per-row trust stamp here — a
// bundle item's posture is read and changed through the bundle surfaces, and a
// second display of it would be a second mechanism.
func TestMcpListRows_CarriesSourceBundle(t *testing.T) {
	servers := []operations.MCPServerEntry{
		{Name: "ctxloom", Command: "/abs/ctxloom", Args: []string{"mcp", "serve"}, Source: "ctxloom+builtin:ctxloom-mcp"},
		{Name: "other", Command: "other-cmd", Source: "ctxloom+git://example.invalid/kit//bundles/tools"},
	}

	rows := mcpListRows(servers)

	require.Len(t, rows, 2, "one row per server, in server order")
	assert.Equal(t, "ctxloom", rows[0].Name)
	assert.Equal(t, "ctxloom+builtin:ctxloom-mcp", rows[0].Source)
	assert.Equal(t, "other", rows[1].Name)
	assert.Equal(t, "ctxloom+git://example.invalid/kit//bundles/tools", rows[1].Source)

	b, err := json.Marshal(rows[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), `"source":"ctxloom+builtin:ctxloom-mcp"`)
}
