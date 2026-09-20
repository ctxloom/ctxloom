package bundles

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// closureRoot is the smallest possible trust.TrustRoot: a function. The readers
// take the PORT (trust.TrustRoot), never the allowed_signers store, so a test
// can answer the one policy question in a closure and no adapter is needed.
type closureRoot func(key ssh.PublicKey, ns string, now time.Time) trust.SignerDecision

func (f closureRoot) TrustedForNamespace(key ssh.PublicKey, ns string, now time.Time) trust.SignerDecision {
	return f(key, ns, now)
}

// TestReader_ConsultsTheTrustPort_AndStampsItsPrincipal: the reader resolves
// the signer through whatever answers trust.TrustRoot, and what it stamps on
// the bundle is the PORT's answer — the principal the port names, not
// anything the artifact says about itself.
func TestReader_ConsultsTheTrustPort_AndStampsItsPrincipal(t *testing.T) {
	fsys, dir := stageUnsignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	signer, pub := testSkillSigner(t)
	signTreeManifest(t, fsys, "/bundles", signer)

	var asked []string
	root := closureRoot(func(key ssh.PublicKey, ns string, _ time.Time) trust.SignerDecision {
		asked = append(asked, ns)
		if bytes.Equal(key.Marshal(), pub.Marshal()) {
			return trust.SignerDecision{Trusted: true, Principal: "closure@example.test"}
		}
		return trust.SignerDecision{Reason: "not the signing key"}
	})

	read := readTree(t, fsys, "/bundles", root)

	require.NotEmpty(t, asked, "the reader never consulted the trust port for %s", dir)
	assert.Equal(t, SignatureValid, read.Signature())
	assert.Equal(t, SignerTrusted, read.Signer())
	assert.Equal(t, "closure@example.test", read.Bundle.Signer(),
		"the stamped signer must be the principal the PORT named")
}

// TestReader_TrustPortSaysNo_ReadsUnsignedToUs: a port that trusts no key
// makes a well-formed signature read as unsigned-to-us — the review path —
// never as an identity.
func TestReader_TrustPortSaysNo_ReadsUnsignedToUs(t *testing.T) {
	fsys, _ := stageUnsignedTree(t, "/bundles", "FRAG-BODY-MARKER")
	signer, _ := testSkillSigner(t)
	signTreeManifest(t, fsys, "/bundles", signer)

	root := closureRoot(func(ssh.PublicKey, string, time.Time) trust.SignerDecision {
		return trust.SignerDecision{Reason: "nobody is trusted"}
	})

	read := readTree(t, fsys, "/bundles", root)

	assert.Equal(t, SignerUntrusted, read.Signer())
	assert.Empty(t, read.Bundle.Signer())
}
