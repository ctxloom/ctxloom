package config

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// The ctxloom release/publisher public key embedded in
// embedded_signers.allowed_signers. If the embedded file changes, this must too.
const ctxloomReleasePubkey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGEOgyPlwr63FRcLm0AfbN+Cmn1JwEPjrbYmTv+ddF+P ben+ctxloom@abbitt.me"

func parseAuthorizedKey(t *testing.T, line string) ssh.PublicKey {
	t.Helper()
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)
	return pk
}

func TestEmbeddedSigners_ReleaseKeyTrustedForPublishOnly(t *testing.T) {
	store := embeddedSigners()
	require.NotNil(t, store)

	key := parseAuthorizedKey(t, ctxloomReleasePubkey)
	now := time.Now()

	// Trusted to PUBLISH — it signs bundles.
	pub := store.TrustedForNamespace(key, signing.NamespacePublish, now)
	assert.True(t, pub.Trusted, "release key must be trusted for the publish namespace")

	// NOT trusted to APPROVE or REJECT — the release key cannot countersign a
	// user's approval. Role separation is the whole point of the namespaces.
	appr := store.TrustedForNamespace(key, signing.NamespaceApprove, now)
	assert.False(t, appr.Trusted, "release key must NOT be trusted to approve")
	rej := store.TrustedForNamespace(key, signing.NamespaceReject, now)
	assert.False(t, rej.Trusted, "release key must NOT be trusted to reject")
}

func TestEmbeddedSigners_UnknownKeyTrustsNothing(t *testing.T) {
	store := embeddedSigners()
	// A real, valid ed25519 key that is simply not the embedded one (fixed seed
	// → deterministic, no rand dependency).
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	require.NoError(t, err)
	d := store.TrustedForNamespace(pub, signing.NamespacePublish, time.Now())
	assert.False(t, d.Trusted, "a key not in the embedded root must be trusted for nothing")
}
