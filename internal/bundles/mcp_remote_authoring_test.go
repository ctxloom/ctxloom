package bundles

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
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

// The preimage covers URL and Headers, so an approval of a remote server binds
// to WHERE it dials and WHAT CREDENTIAL it presents. Rewriting either after
// approval stops verifying — fail-closed, back to pending.
//
// This is the whole reason the fields could not simply be added: an approval
// that survived a header rewrite would be an approval of nothing in particular.
func TestExecContentPayload_RemoteApprovalStopsVerifyingWhenAHeaderIsRewritten(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	const ref = "my-tools#mcp/remote-tools"
	approved := BundleMCP{
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer t0ken"},
	}

	payload, err := approved.ContentPayload()
	require.NoError(t, err)
	assert.Contains(t, string(payload), "https://mcp.example.com/mcp", "the endpoint is inside the signed bytes")
	assert.Contains(t, string(payload), "Bearer t0ken", "the header is inside the signed bytes")

	framed := signing.ApproveCountersignPayload(ref, signing.AttestExecMCP, payload)
	armored, err := signing.Sign(framed, signer, signing.NamespaceApprove)
	require.NoError(t, err)
	require.NoError(t, signing.Verify(framed, armored, sshPub, signing.NamespaceApprove))

	// Same server, one header rewritten: the credential now leaks to whatever
	// that value names. The old approval must not cover it.
	rewritten := BundleMCP{
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer attacker"},
	}
	rewrittenPayload, err := rewritten.ContentPayload()
	require.NoError(t, err)
	assert.NotEqual(t, payload, rewrittenPayload)
	reframed := signing.ApproveCountersignPayload(ref, signing.AttestExecMCP, rewrittenPayload)
	assert.Error(t, signing.Verify(reframed, armored, sshPub, signing.NamespaceApprove),
		"an approval must not survive a rewritten header")

	// Same server, redirected to another host: likewise.
	redirected := BundleMCP{
		URL:     "https://evil.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer t0ken"},
	}
	redirectedPayload, err := redirected.ContentPayload()
	require.NoError(t, err)
	reframedURL := signing.ApproveCountersignPayload(ref, signing.AttestExecMCP, redirectedPayload)
	assert.Error(t, signing.Verify(reframedURL, armored, sshPub, signing.NamespaceApprove),
		"an approval must not survive a redirected endpoint")
}

// Headers is a map, so its iteration order must not reach the signed bytes.
// encoding/json sorts map keys; this is what holds that guarantee if the
// builder is ever hand-rolled — the same contract Env carries.
func TestExecContentPayload_HeaderKeyOrderDoesNotMoveThePreimage(t *testing.T) {
	first := BundleMCP{
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5"},
	}
	reordered := BundleMCP{
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"E": "5", "D": "4", "C": "3", "B": "2", "A": "1"},
	}
	a, err := first.ContentPayload()
	require.NoError(t, err)
	b, err := reordered.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, a, b, "header key order must not change the preimage bytes")
}

// A stdio entry and a remote entry are different servers and must never share a
// trust identity, whatever else they have in common.
func TestExecContentPayload_StdioAndRemoteNeverShareATrustIdentity(t *testing.T) {
	stdio := BundleMCP{Command: "srv"}
	remote := BundleMCP{URL: "https://mcp.example.com/mcp"}
	assert.NotEqual(t, stdio.ComputeContentHash(), remote.ComputeContentHash())
}
