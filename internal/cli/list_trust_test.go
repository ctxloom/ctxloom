package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
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
	assert.Contains(t, string(b), `"state":"accepted"`, "an exempt allow renders state accepted")
}

// TestStampItemTrust_RejectedFragmentJSON proves the json row reflects a
// rejected local fragment as withheld (trusted=false, source=rejected,
// state=rejected).
func TestStampItemTrust_RejectedFragmentJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	store := loadTrustStore(t, appDir)
	require.NoError(t, store.SetRejected(remote.LocalSource, "demo#fragments/curl-pipe-sh"))

	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeFragment, rows)

	row, ok := findRow(rows, "demo#fragments/curl-pipe-sh")
	require.True(t, ok)
	assert.False(t, row.Trusted)
	assert.Equal(t, "rejected", row.TrustSource)
	assert.Equal(t, "rejected", row.State)
}

// TestStampItemTrust_LocalSkillJSON is the regression guard for the prompt->skill
// rename: the CLI list emits #skills/<name> refs, and a project-authored local
// skill must resolve trusted via the local exemption — not fail-closed to
// trusted:false/pending because the trust selector didn't know the "skills" kind.
func TestStampItemTrust_LocalSkillJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalSkill(t, cfg, "demo", "review", "always-local skill body")

	rows, err := listItemRows(cfg, ItemTypeSkill)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeSkill, rows)

	row, ok := findRow(rows, "demo#skills/review")
	require.True(t, ok, "seeded local skill must be listed")
	assert.True(t, row.Trusted, "local content is auto-allowed")
	assert.Equal(t, "local", row.TrustSource)

	b, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"trusted":true`)
	assert.Contains(t, string(b), `"trust_source":"local"`)
}

// TestStampItemTrust_RejectedSkillJSON proves the #skills/ row reflects a
// rejected local skill as withheld. The stamp queries the store by the
// canonical key (Kind.Dir()=="prompts" for a skill), so the rejection is stored
// under #prompts/ — exactly what operations.SetBlacklist canonicalizes a
// user's `blacklist demo#skills/review` to.
func TestStampItemTrust_RejectedSkillJSON(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalSkill(t, cfg, "demo", "review", "rm -rf danger")

	store := loadTrustStore(t, appDir)
	require.NoError(t, store.SetRejected(remote.LocalSource, "demo#prompts/review"))

	rows, err := listItemRows(cfg, ItemTypeSkill)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeSkill, rows)

	row, ok := findRow(rows, "demo#skills/review")
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
	cfg := &config.Config{AppPaths: []string{appDir}}

	servers := []operations.MCPServerEntry{{Name: "local-srv", Command: "node", Backend: "unified"}}
	rows := []mcpListRow{{Name: "local-srv", Command: "node", Backend: "unified"}}
	stampMCPTrust(cfg, servers, rows)

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
	cfg := &config.Config{AppPaths: []string{appDir}}

	srv := operations.MCPServerEntry{Name: "local-srv", Command: "node", Args: []string{"-x"}, Backend: "unified"}
	mcp := bundles.BundleMCP{Command: srv.Command, Args: srv.Args}

	store := loadTrustStore(t, appDir)
	// The rejection is recorded under a DIFFERENT ref; the content denylist
	// still catches the identical executable surface by hash.
	require.NoError(t, store.SetRejected(remote.LocalSource, "#mcp/other-name", mcp.ComputeContentHash()))

	rows := []mcpListRow{{Name: srv.Name, Command: srv.Command, Args: srv.Args, Backend: srv.Backend}}
	stampMCPTrust(cfg, []operations.MCPServerEntry{srv}, rows)

	assert.False(t, rows[0].Trusted)
	assert.Equal(t, "rejected", rows[0].TrustSource)
	assert.Equal(t, "rejected", rows[0].State)
}
