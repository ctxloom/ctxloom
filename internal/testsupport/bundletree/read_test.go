package bundletree

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// Each Signing mode must produce the facts it names through the production
// readers — a mode that silently produced different facts would make every
// test built on it assert against the wrong case.
func TestRemoteRead_EachSigningYieldsItsFacts(t *testing.T) {
	const ref = "https://example.test/repo@bundles/tools"
	for _, tc := range []struct {
		name   string
		s      Signing
		sig    bundles.Signature
		signer bundles.Signer
	}{
		{"unsigned", Unsigned, bundles.SignatureNone, bundles.SignerNone},
		{"trusted", SignedByTrustedKey, bundles.SignatureValid, bundles.SignerTrusted},
		{"untrusted", SignedByUntrustedKey, bundles.SignatureValid, bundles.SignerUntrusted},
		{"edited trusted", EditedAfterTrustedSigning, bundles.SignatureInvalid, bundles.SignerTrusted},
		{"edited untrusted", EditedAfterUntrustedSigning, bundles.SignatureInvalid, bundles.SignerUntrusted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := RemoteRead(t, ref, oneFragment(), tc.s)
			assert.True(t, read.Claimed())
			assert.Equal(t, bundles.ProvenanceRemote, read.Provenance)
			assert.Equal(t, bundles.TrustCtxRemote, read.TrustCtx())
			assert.Equal(t, tc.sig, read.Signature())
			assert.Equal(t, tc.signer, read.Signer())
		})
	}
	assert.Equal(t, Publisher, RemoteRead(t, ref, oneFragment(), SignedByTrustedKey).Bundle.Signer())
}

func TestProjectRead_EachSigningYieldsItsFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Signing
		sig  bundles.Signature
	}{
		{"unsigned", Unsigned, bundles.SignatureNone},
		{"trusted", SignedByTrustedKey, bundles.SignatureValid},
		{"edited", EditedAfterTrustedSigning, bundles.SignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := ProjectRead(t, "kit", &bundles.Bundle{}, tc.s)
			assert.True(t, read.Claimed())
			assert.Equal(t, bundles.ProvenanceProject, read.Provenance)
			assert.Equal(t, bundles.TrustCtxLocal, read.TrustCtx())
			assert.Equal(t, tc.sig, read.Signature())
		})
	}
}

// oneFragment is the smallest bundle a remote tree may hold: the repofs reader
// refuses a tree that declares no items.
func oneFragment() *bundles.Bundle {
	return &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"f": {ItemBody: bundles.ItemBody{Content: "x"}}}}
}

// The trust root PublisherKey returns grants its key exactly the principal
// asked for, in the publish namespace — the identity a reader then resolves a
// signature to.
func TestPublisherKey_TrustsTheKeyAsThePrincipalAsked(t *testing.T) {
	const principal = "someone@example.test"
	_, root, pub := PublisherKey(t, principal)
	got := root.TrustedForNamespace(pub, signing.NamespacePublish, time.Now())
	assert.True(t, got.Trusted)
	assert.Equal(t, principal, got.Principal)
}
