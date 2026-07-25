package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// newTestSigner returns an ephemeral in-memory ed25519 ssh.Signer and its
// ssh.PublicKey, for tests that don't need a real on-disk key or an agent.
func newTestSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer, signer.PublicKey()
}

func TestSignVerify_RoundTrip(t *testing.T) {
	signer, pub := newTestSigner(t)
	payload := []byte("the exact bytes of a bundle file")

	armored, err := Sign(payload, signer, "publish.v1.ctxloom.dev")
	require.NoError(t, err)
	assert.Contains(t, string(armored), "-----BEGIN SSH SIGNATURE-----")

	err = Verify(payload, armored, pub, "publish.v1.ctxloom.dev")
	assert.NoError(t, err)
}

func TestVerify_RejectsWrongNamespace(t *testing.T) {
	signer, pub := newTestSigner(t)
	payload := []byte("payload")

	armored, err := Sign(payload, signer, "approve.v1.ctxloom.dev")
	require.NoError(t, err)

	// Namespaces are the domain separator: a signature made under one
	// namespace must not verify under another (spec §1, §12).
	err = Verify(payload, armored, pub, "reject.v1.ctxloom.dev")
	assert.Error(t, err)
}

func TestVerify_RejectsTamperedPayload(t *testing.T) {
	signer, pub := newTestSigner(t)
	payload := []byte("original bytes")

	armored, err := Sign(payload, signer, "publish.v1.ctxloom.dev")
	require.NoError(t, err)

	tampered := []byte("original byteZ")
	err = Verify(tampered, armored, pub, "publish.v1.ctxloom.dev")
	assert.Error(t, err)
}

func TestVerify_RejectsWrongKey(t *testing.T) {
	signer, _ := newTestSigner(t)
	_, otherPub := newTestSigner(t)
	payload := []byte("payload")

	armored, err := Sign(payload, signer, "publish.v1.ctxloom.dev")
	require.NoError(t, err)

	err = Verify(payload, armored, otherPub, "publish.v1.ctxloom.dev")
	assert.Error(t, err)
}

func TestVerify_RejectsCorruptedSignatureBody(t *testing.T) {
	// Implementer trap #2 (spec §14.2): a store entry with a correct
	// filename/hash and a corrupted signature body must resolve pending, not
	// approved. At this layer that means: corrupt bytes must not verify.
	signer, pub := newTestSigner(t)
	payload := []byte("payload")

	armored, err := Sign(payload, signer, "publish.v1.ctxloom.dev")
	require.NoError(t, err)

	corrupted := append([]byte(nil), armored...)
	// Flip a byte inside the PEM body (well past the "-----BEGIN..." header).
	for i := len(corrupted) - 20; i < len(corrupted); i++ {
		if corrupted[i] != '\n' && corrupted[i] != '-' {
			corrupted[i] ^= 0xFF
			break
		}
	}

	err = Verify(payload, corrupted, pub, "publish.v1.ctxloom.dev")
	assert.Error(t, err)
}

func TestVerify_RejectsMalformedArmor(t *testing.T) {
	_, pub := newTestSigner(t)
	err := Verify([]byte("payload"), []byte("not a signature at all"), pub, "publish.v1.ctxloom.dev")
	assert.Error(t, err)
}

func TestSign_EmptyNamespaceRejected(t *testing.T) {
	signer, _ := newTestSigner(t)
	_, err := Sign([]byte("payload"), signer, "")
	assert.Error(t, err)
}

// U134-F01: Sign hashed and signed a ZERO-BYTE payload and reported success.
// sshsig.Sign validates only that the namespace is non-empty, then io.Copy's an
// empty reader into the hash — producing a valid armored signature over the
// empty string. `ctxloom sign` on a truncated bundle wrote that .sig and printed
// success, and the blob verifies over EVERY zero-byte bundle, for every
// consumer, forever.
//
// The floor belongs in the primitive: a floor only in operations.SignBundleFile
// would leave the skills and bundles callers exposed.
func TestSign_RefusesAnEmptyPayload(t *testing.T) {
	signer, _ := newTestSigner(t)
	for _, payload := range [][]byte{nil, {}} {
		out, err := Sign(payload, signer, NamespacePublish)
		require.Error(t, err, "a signature over zero bytes attests to nothing")
		assert.Nil(t, out)
	}
}
