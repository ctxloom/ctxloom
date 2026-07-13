package bundles

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// =============================================================================
// The versioned exec preimage (signature envelope spec §3.3.2, §12).
//
// The exec preimage is the ONE place the spec's "we never canonicalize" rule
// cannot be honored: MCP servers and hooks have no raw bytes, only structured
// fields, so their preimage IS a canonical JSON encoding. The hazard that
// creates is named in §3.3.2: adding a field to BundleMCP silently changes the
// preimage and silently invalidates every approval of every MCP server, sending
// them all back to pending with no version signal. Fail-closed, but a nasty
// surprise.
//
// The mitigation is a version carrier: `"preimage":"ctxloom-exec/1"` as the
// FIRST field of the canonical struct. Bumping it becomes a deliberate,
// announced act. These tests hold that contract — the literal string, its
// position, and the determinism the signature depends on.
// =============================================================================

// The exec preimage's first field must be the contract version, for BOTH exec
// item kinds. Position is part of the contract (spec §3.3.2: "gains a
// `"preimage":"ctxloom-exec/1"` FIRST field"), so this asserts exact bytes
// rather than JSONEq — a version carrier buried mid-object would satisfy an
// order-insensitive compare and still be wrong.
func TestExecContentPayload_IsVersioned_MCP(t *testing.T) {
	mcp := BundleMCP{
		Command:      "postgres-mcp",
		Args:         []string{"--host", "db"},
		Env:          map[string]string{"PGUSER": "admin"},
		Installation: "npm i -g postgres-mcp",
		Notes:        "human-only, excluded",
	}

	payload, err := mcp.ContentPayload()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(string(payload), `{"preimage":"`+signing.ExecPreimageContract+`"`),
		"the exec preimage must OPEN with the contract version; got: %s", payload)
	assert.Equal(t,
		`{"preimage":"ctxloom-exec/1","command":"postgres-mcp","args":["--host","db"],`+
			`"env":{"PGUSER":"admin"},"installation":"npm i -g postgres-mcp"}`,
		string(payload))
}

func TestExecContentPayload_IsVersioned_Hook(t *testing.T) {
	hook := BundleHook{
		Matcher:         "Bash",
		Type:            "command",
		Command:         "echo hi",
		PreToolFallback: true,
	}

	payload, err := hook.ContentPayload()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(string(payload), `{"preimage":"`+signing.ExecPreimageContract+`"`),
		"the exec preimage must OPEN with the contract version; got: %s", payload)
	assert.Equal(t,
		`{"preimage":"ctxloom-exec/1","matcher":"Bash","type":"command","command":"echo hi",`+
			`"prompt":"","pre_tool_fallback":true}`,
		string(payload))
}

// The version carrier is a real first key, not a prefix coincidence: decoding
// the preimage into an ordered key list must yield "preimage" at index 0.
func TestExecContentPayload_VersionIsFirstDecodedKey(t *testing.T) {
	mcpPayload, err := (&BundleMCP{Command: "x"}).ContentPayload()
	require.NoError(t, err)
	hookPayload, err := (&BundleHook{Command: "x"}).ContentPayload()
	require.NoError(t, err)

	for _, payload := range [][]byte{mcpPayload, hookPayload} {
		dec := json.NewDecoder(strings.NewReader(string(payload)))
		_, err := dec.Token() // consume '{'
		require.NoError(t, err)
		first, err := dec.Token() // first key
		require.NoError(t, err)
		assert.Equal(t, "preimage", first)

		val, err := dec.Token()
		require.NoError(t, err)
		assert.Equal(t, signing.ExecPreimageContract, val)
	}
}

// A non-deterministic preimage is a signature bug: the same item must always
// produce identical bytes, and a map's iteration order must never leak into
// them (encoding/json sorts map keys — this test is what holds that guarantee
// if the builder is ever hand-rolled).
func TestExecContentPayload_IsDeterministic(t *testing.T) {
	mcp := BundleMCP{
		Command: "srv",
		Args:    []string{"--a", "--b"},
		Env:     map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5"},
	}
	first, err := mcp.ContentPayload()
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		again, err := mcp.ContentPayload()
		require.NoError(t, err)
		assert.Equal(t, first, again, "exec preimage must be byte-identical across builds")
	}

	// Same item, Env literal written in a different order: same bytes.
	reordered := BundleMCP{
		Command: "srv",
		Args:    []string{"--a", "--b"},
		Env:     map[string]string{"E": "5", "D": "4", "C": "3", "B": "2", "A": "1"},
	}
	reorderedPayload, err := reordered.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, first, reorderedPayload, "env key order must not change the preimage bytes")

	hook := BundleHook{Matcher: "Bash", Type: "command", Command: "echo hi"}
	hookFirst, err := hook.ContentPayload()
	require.NoError(t, err)
	hookAgain, err := hook.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, hookFirst, hookAgain)
}

// Round-trip through the real countersign path: an exec item approved under the
// versioned preimage verifies, and the framing is exactly what the countersign
// path expects (spec §3.2 — kind mcp/hooks, form "exec", payload = the bytes
// ContentPayload returns and nothing re-derived).
func TestExecContentPayload_ApproveCountersignRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	mcp := BundleMCP{Command: "postgres-mcp", Args: []string{"--host", "db"}}
	const ref = "my-tools#mcp/postgres"

	payload, err := mcp.ContentPayload()
	require.NoError(t, err)

	// The approver signs the framed countersign payload over the exec preimage.
	framed := signing.ApproveCountersignPayload(signing.KindMCP, ref, signing.FormExec, payload)
	armored, err := signing.Sign(framed, signer, signing.NamespaceApprove)
	require.NoError(t, err)

	// A verifier re-derives the frame from the item it is about to expose. It
	// must land on identical bytes — that is the whole contract.
	rederived, err := mcp.ContentPayload()
	require.NoError(t, err)
	reframed := signing.ApproveCountersignPayload(signing.KindMCP, ref, signing.FormExec, rederived)
	assert.Equal(t, framed, reframed)
	require.NoError(t, signing.Verify(reframed, armored, sshPub, signing.NamespaceApprove))

	// The version travels INSIDE the signed bytes, so a preimage bump is
	// cryptographically visible rather than a silent re-review.
	assert.Contains(t, string(framed), signing.ExecPreimageContract)

	// And the mass-invalidation property the version exists to make deliberate:
	// change the field set (here, simulated by a changed executable surface) and
	// the old approval no longer verifies. Fail-closed, back to pending.
	changed := BundleMCP{Command: "postgres-mcp", Args: []string{"--host", "evil"}}
	changedPayload, err := changed.ContentPayload()
	require.NoError(t, err)
	changedFrame := signing.ApproveCountersignPayload(signing.KindMCP, ref, signing.FormExec, changedPayload)
	assert.Error(t, signing.Verify(changedFrame, armored, sshPub, signing.NamespaceApprove))
}

// ComputeContentHash must keep hashing EXACTLY ContentPayload's output after
// versioning — one definition of "the bytes of this item", never two (spec
// §3.2). If the version were added to the signing path but not the hash path,
// the store index and the signature would disagree.
func TestExecContentPayload_RemainsTheHashPreimage(t *testing.T) {
	mcp := BundleMCP{Command: "srv", Env: map[string]string{"K": "V"}}
	mcpPayload, err := mcp.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, hashContent(mcpPayload), mcp.ComputeContentHash())

	hook := BundleHook{Matcher: "Bash", Type: "command", Command: "echo hi"}
	hookPayload, err := hook.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, hashContent(hookPayload), hook.ComputeContentHash())
}
