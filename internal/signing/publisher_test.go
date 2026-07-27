package signing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// rootWith builds a real allowed_signers store trusting pub as principal for
// exactly the listed namespaces. A real store (not a fake) is used here on
// purpose: the namespace/role check is the thing under test, and stubbing it
// out would test nothing.
func rootWith(principal string, pub ssh.PublicKey, namespaces ...string) *allowedsigners.Store {
	return allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: namespaces,
		PublicKey:  pub,
	})
}

func TestVerifyPublisher_VerifiedByTrustedKey(t *testing.T) {
	signer, pub := newTestSigner(t)
	bundle := []byte("name: go-tools\nfragments:\n  go-testing:\n    content: hello\n")

	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)

	principal, err := VerifyPublisher(bundle, sig, rootWith("bundles@ctxloom.dev", pub, NamespacePublish), time.Now())
	require.NoError(t, err)
	assert.Equal(t, "bundles@ctxloom.dev", principal,
		"a signature by a key trusted for the publish namespace, over exactly these bytes, yields that key's principal")
}

func TestVerifyPublisher_NoSignatureIsUnsignedNotAnError(t *testing.T) {
	_, pub := newTestSigner(t)
	bundle := []byte("name: go-tools\n")

	// No .sig at the pinned SHA (spec §10.1). Unsigned content is legal,
	// ordinary, and the common case — it takes the review path.
	principal, err := VerifyPublisher(bundle, nil, rootWith("bundles@ctxloom.dev", pub, NamespacePublish), time.Now())
	require.NoError(t, err, "absent signature is not an error — it is unsigned content")
	assert.Empty(t, principal)
}

func TestVerifyPublisher_UntrustedKeyIsUnsignedNotAnError(t *testing.T) {
	signer, _ := newTestSigner(t)
	_, otherPub := newTestSigner(t)
	bundle := []byte("name: third-party\n")

	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)

	// The signature is perfectly valid — but it is by a key we have never
	// heard of. That is "unsigned content TO YOU" (spec §10.2), and it must
	// be quiet: no error, no tamper signal, just the review path.
	root := rootWith("someone-else@example.com", otherPub, NamespacePublish)
	principal, err := VerifyPublisher(bundle, sig, root, time.Now())
	require.NoError(t, err, "a valid signature by an untrusted key is unsigned content, not an error")
	assert.Empty(t, principal)
}

// TRAP #3. A valid publish-namespace signature from a key that allowed_signers
// only authorizes to APPROVE is not a publish authorization.
func TestVerifyPublisher_KeyNotTrustedForPublishNamespace(t *testing.T) {
	signer, pub := newTestSigner(t)
	bundle := []byte("name: go-tools\n")

	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)

	// The key IS in allowed_signers, and the signature IS cryptographically
	// valid over these exact bytes under the publish namespace. It is still
	// not a publisher: this key is trusted only to approve and reject.
	root := rootWith("lead@team.example", pub, NamespaceApprove, NamespaceReject)
	principal, err := VerifyPublisher(bundle, sig, root, time.Now())
	require.NoError(t, err, "an approve-only key that signed a publish assertion is untrusted, not tampered")
	assert.Empty(t, principal, "an approve-only key must never authorize published content")
}

// TRAP: the advisory identity in the artifact is never the identity we report.
// The principal always comes from the matched allowed_signers entry.
func TestVerifyPublisher_PrincipalComesFromTrustRootNotTheArtifact(t *testing.T) {
	signer, pub := newTestSigner(t)
	// The bundle bytes themselves claim to be from the ctxloom release key.
	bundle := []byte("name: evil\nsigner: releases@ctxloom.dev\n")

	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)

	root := rootWith("mallory@example.com", pub, NamespacePublish)
	principal, err := VerifyPublisher(bundle, sig, root, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "mallory@example.com", principal,
		"the signer identity is resolved from allowed_signers, never from a field anyone can write into the file")
}

// TAMPER (spec §10.2): a trusted key's signature that does not cover these
// bytes. Loud — the bundle is withheld entirely, never downgraded to unsigned.
func TestVerifyPublisher_TrustedKeyOverDifferentBytesIsTamper(t *testing.T) {
	signer, pub := newTestSigner(t)

	sig, err := Sign([]byte("the bytes that were signed"), signer, NamespacePublish)
	require.NoError(t, err)

	principal, err := VerifyPublisher([]byte("the bytes that arrived"), sig,
		rootWith("bundles@ctxloom.dev", pub, NamespacePublish), time.Now())
	require.ErrorIs(t, err, ErrSignatureTampered)
	assert.Empty(t, principal)
}

// TAMPER: a structurally invalid blob. An attacker must not be able to
// downgrade a signed bundle to an unsigned one by corrupting its signature.
func TestVerifyPublisher_StructurallyInvalidSignatureIsTamper(t *testing.T) {
	_, pub := newTestSigner(t)

	principal, err := VerifyPublisher([]byte("bundle"), []byte("-----BEGIN SSH SIGNATURE-----\ngarbage\n"),
		rootWith("bundles@ctxloom.dev", pub, NamespacePublish), time.Now())
	require.ErrorIs(t, err, ErrSignatureTampered)
	assert.Empty(t, principal)
}

// A publish signature must not be forgeable by replaying an approve signature
// over the same bytes: the namespace is inside the signed blob.
func TestVerifyPublisher_ApproveSignatureIsNotAPublishSignature(t *testing.T) {
	signer, pub := newTestSigner(t)
	bundle := []byte("name: go-tools\n")

	// Signed under the APPROVE namespace by a key trusted to publish AND
	// approve. Presented as a publisher signature, it must not verify.
	sig, err := Sign(bundle, signer, NamespaceApprove)
	require.NoError(t, err)

	root := rootWith("ben@abbitt.me", pub, NamespacePublish, NamespaceApprove)
	principal, err := VerifyPublisher(bundle, sig, root, time.Now())
	require.ErrorIs(t, err, ErrSignatureTampered,
		"a signature whose embedded namespace is not publish cannot authenticate published content")
	assert.Empty(t, principal)
}

// U134-F04: a nil trust root must not let a STRUCTURALLY INVALID signature
// blob downgrade to "unsigned to you" — that is exactly the corrupt-.sig
// downgrade TestVerifyPublisher_StructurallyInvalidSignatureIsTamper guards
// against for a non-nil root. The nil-root branch used to run BEFORE the
// unarmor check, so a nil root swallowed this case too: `("", nil)` for
// EVERY input, including a blob that cannot even be parsed as a signature —
// the exact signed→unsigned downgrade the package's own doc says must never
// happen.
func TestVerifyPublisher_NilRootWithCorruptSignatureIsStillTamper(t *testing.T) {
	principal, err := VerifyPublisher([]byte("bundle"), []byte("-----BEGIN SSH SIGNATURE-----\ngarbage\n"), nil, time.Now())
	require.ErrorIs(t, err, ErrSignatureTampered)
	assert.Empty(t, principal)
}

// A nil trust root trusts nothing — every signature is unsigned content.
// Deleting allowed_signers denies everything (spec §10.5: every deletion moves
// toward less exposure).
func TestVerifyPublisher_NilTrustRootTrustsNothing(t *testing.T) {
	signer, _ := newTestSigner(t)
	bundle := []byte("name: go-tools\n")
	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)

	principal, err := VerifyPublisher(bundle, sig, allowedsigners.NewStore(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, principal)
}
