package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Bundle-authored REMOTE MCP entries (url + headers).
//
// wire.MCPServer has carried URL and Headers for a while, but only a RUNTIME
// caller could populate them: a bundle YAML could not declare a remote server
// at all, because BundleMCP had no such fields and ParseBundle's strict decode
// therefore refused the keys.
//
// Closing that gap is not a plain field addition. The executable trust preimage
// (mcpContentPayload) must cover the new fields, or a bundle could redirect an
// approved server to another host — or rewrite its Authorization header — under
// an approval that still verifies. Covering them changes the preimage bytes for
// EVERY mcp item, which invalidates every recorded MCP approval in the field;
// that is exactly the event signing.ExecPreimageContract exists to announce,
// hence the bump to ctxloom-exec/2.
// =============================================================================

// A remote entry is AUTHORABLE: the two keys decode, and nothing about being
// remote makes the entry second-class.
func TestParseBundle_MCPEntryDeclaresURLAndHeaders(t *testing.T) {
	b, err := ParseBundle([]byte(`
version: "1.0"
mcp:
  remote-tools:
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer t0ken
      X-Tenant: acme
`))
	require.NoError(t, err)

	srv, ok := b.MCP["remote-tools"]
	require.True(t, ok, "a bundle must be able to declare a remote MCP entry")
	assert.Equal(t, "https://mcp.example.com/mcp", srv.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer t0ken", "X-Tenant": "acme"}, srv.Headers)
	assert.Empty(t, srv.Command, "a remote entry carries no command")
}

// The one-of-Command|URL rule is wire.MCPServer's, and it must hold for
// bundle-authored entries too — enforced at LOAD, fail-loud, so a malformed
// entry cannot reach a gate or an engine writer and be resolved by guesswork.
func TestParseBundle_MCPEntryWithBothCommandAndURLIsRefusedAtLoad(t *testing.T) {
	_, err := ParseBundle([]byte(`
version: "1.0"
mcp:
  confused:
    command: /usr/bin/srv
    url: https://mcp.example.com/mcp
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confused", "the diagnostic must name the offending entry")
}

func TestParseBundle_MCPEntryWithNeitherCommandNorURLIsRefusedAtLoad(t *testing.T) {
	_, err := ParseBundle([]byte(`
version: "1.0"
mcp:
  empty:
    notes: nothing to launch and nothing to dial
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestParseBundle_MCPEntryWithNonDialableURLSchemeIsRefusedAtLoad(t *testing.T) {
	_, err := ParseBundle([]byte(`
version: "1.0"
mcp:
  ftp-server:
    url: ftp://mcp.example.com/mcp
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ftp-server")
}
