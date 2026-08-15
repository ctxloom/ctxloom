package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// newSuppressionTestSigner returns an ephemeral in-memory ed25519 ssh.Signer
// and its ssh.PublicKey — the same throwaway-keypair pattern
// signing.newTestSigner uses, reimplemented here (unexported, package-local)
// so this test never needs ctxloom's actual, un-forgeable production private
// key to prove the SUBTRACTION mechanism works.
func newSuppressionTestSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer, signer.PublicKey()
}

// TestFilterSuppressedPrincipals_RemovesMatchingEntry is the pure unit test of
// the actual subtraction primitive oozy-plod (b) needed: a store containing a
// trusted publish-namespace key stops trusting that key once its principal is
// suppressed, and an UNRELATED principal in the same store is left alone. A
// real generated keypair stands in for "the embedded key" — the mechanism is
// principal-driven and agnostic to which key it operates on, so this proves
// it generically without needing ctxloom's actual production private key
// (which this repo never has — only the PUBLIC half is embedded).
func TestFilterSuppressedPrincipals_RemovesMatchingEntry(t *testing.T) {
	_, pubA := newSuppressionTestSigner(t)
	_, pubB := newSuppressionTestSigner(t)
	entryA := allowedsigners.Entry{
		Principals: []string{"vendor@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		KeyType:    pubA.Type(),
		PublicKey:  pubA,
	}
	entryB := allowedsigners.Entry{
		Principals: []string{"other@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		KeyType:    pubB.Type(),
		PublicKey:  pubB,
	}
	store := allowedsigners.NewStore(entryA, entryB)
	now := time.Now()

	// Before: both trusted.
	assert.True(t, store.TrustedForNamespace(pubA, signing.NamespacePublish, now).Trusted)
	assert.True(t, store.TrustedForNamespace(pubB, signing.NamespacePublish, now).Trusted)

	filtered := filterSuppressedPrincipals(store, map[string]bool{"vendor@example.com": true})

	assert.False(t, filtered.TrustedForNamespace(pubA, signing.NamespacePublish, now).Trusted,
		"a suppressed principal's key must no longer be trusted")
	assert.True(t, filtered.TrustedForNamespace(pubB, signing.NamespacePublish, now).Trusted,
		"an unrelated principal's key must be untouched by another principal's suppression")
}

// TestFilterSuppressedPrincipals_NilAndEmptyAreNoOps guards the fast paths: a
// nil store and an empty suppression set both return the input unchanged
// (never panic, never fabricate a non-nil empty store where nil was passed).
func TestFilterSuppressedPrincipals_NilAndEmptyAreNoOps(t *testing.T) {
	assert.Nil(t, filterSuppressedPrincipals(nil, map[string]bool{"x": true}))

	store := allowedsigners.NewStore()
	assert.Same(t, store, filterSuppressedPrincipals(store, nil))
}

// TestVerifyPublisher_SuppressedPrincipal_NoLongerVerifies proves the
// end-to-end consequence of the subtraction: content genuinely signed by a
// key stops being recognized as trusted-publisher once that key's principal
// is suppressed from the store handed to signing.VerifyPublisher — the exact
// function every bundle load path (internal/config/config.go's
// verifyBundlePublisher) calls to stamp a bundle's signer. Once VerifyPublisher
// returns "" (unsigned-to-us) instead of the principal,
// operations.EffectiveTrust's step 5 (trusted signer) no longer allows, and
// the item falls to step 7 (pending review) — this is what "content signed
// only by a suppressed embedded key is withheld" cashes out to, mechanically.
func TestVerifyPublisher_SuppressedPrincipal_NoLongerVerifies(t *testing.T) {
	signer, pub := newSuppressionTestSigner(t)
	payload := []byte("a bundle's exact file bytes")
	armored, err := signing.Sign(payload, signer, signing.NamespacePublish)
	require.NoError(t, err)

	entry := allowedsigners.Entry{
		Principals: []string{"vendor@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		KeyType:    pub.Type(),
		PublicKey:  pub,
	}
	store := allowedsigners.NewStore(entry)
	now := time.Now()

	principal, err := signing.VerifyPublisher(payload, armored, store, now)
	require.NoError(t, err)
	assert.Equal(t, "vendor@example.com", principal, "before suppression, the signature verifies as this trusted publisher")

	filtered := filterSuppressedPrincipals(store, map[string]bool{"vendor@example.com": true})
	principal, err = signing.VerifyPublisher(payload, armored, filtered, now)
	require.NoError(t, err, "an untrusted-key signature is UNSIGNED-TO-US, never an error")
	assert.Empty(t, principal, "after suppression, the SAME signature over the SAME bytes no longer verifies as a trusted publisher")
}

// TestTrustRoot_SuppressedEmbeddedPrincipal_NoLongerTrusted is the production
// WIRING proof, using ctxloom's REAL embedded principal/public key
// (ctxloomReleasePubkey, trustroot_embedded_test.go) — the one this repo can
// never forge a signature for, which is exactly why
// TestVerifyPublisher_SuppressedPrincipal_NoLongerVerifies above uses a
// synthetic key to prove the mechanism and this test only needs to prove the
// WIRING: Config.TrustRoot() genuinely reads the on-disk distrusted_signers
// file (via SuppressedEmbeddedPrincipals) and excludes the matching embedded
// entry, using nothing but a public key (TrustedForNamespace never needs a
// signature).
func TestTrustRoot_SuppressedEmbeddedPrincipal_NoLongerTrusted(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	cfg := &Config{appPaths: []string{appDir}}
	cfg.SetFS(fs)

	key := parseAuthorizedKey(t, ctxloomReleasePubkey)
	now := time.Now()

	// Before any suppression: the embedded release key is trusted to publish,
	// exactly as TestEmbeddedSigners_ReleaseKeyTrustedForPublishOnly proves for
	// the raw embedded store — TrustRoot() must agree.
	before := cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now)
	assert.True(t, before.Trusted, "the embedded release key starts out trusted for publish")

	// Write the SAME suppression record `signer remove <embedded-principal>
	// --project` would (operations.RemoveSigner) directly to the project
	// distrusted_signers file, to isolate the TrustRoot()-side read from the
	// CLI/operations write path (that round trip is proven separately in
	// internal/operations).
	require.NoError(t, afero.WriteFile(fs, paths.DistrustedSignersPath(appDir), []byte("ben+ctxloom@abbitt.me\n"), 0o600))

	after := cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now)
	assert.False(t, after.Trusted, "a locally suppressed embedded principal's key must no longer be trusted by TrustRoot()")

	// A blank line and a `#`-comment line must never themselves suppress
	// anything (only an exact principal line does) — guards against a
	// hand-edited file accidentally distrusting nothing, or a stray blank
	// line being read as a (never-matching) empty principal.
	suppressed := cfg.SuppressedEmbeddedPrincipals()
	assert.True(t, suppressed["ben+ctxloom@abbitt.me"])
	assert.False(t, suppressed[""])
}

// An allowed_signers entry may name SEVERAL principals on one line (the
// principals pattern-list is comma-separated). filterSuppressedPrincipals
// removes the WHOLE ENTRY when any one of them is suppressed, so suppressing
// one identity also revokes the grants the same line made to the others.
//
// This test states that semantics rather than endorsing it. Which of the two
// readings is right — "distrusting a principal revokes the LINE" versus
// "distrusting a principal revokes only THAT principal's grant" — decides who
// may grant what after a partial suppression, and that is a trust-model
// decision, not something a sweep settles. Until it is decided, the behaviour
// must at least be visible and stable: changing it here fails this test, which
// is the intended forcing function.
//
// It is latent today (the compiled-in trust root ships one entry naming one
// principal) and reachable the moment a second principal is added to a line.
func TestFilterSuppressedPrincipals_MultiPrincipalEntry_DropsEveryGrantOnTheLine(t *testing.T) {
	_, pub := newSuppressionTestSigner(t)
	entry := allowedsigners.Entry{
		Principals: []string{"vendor@example.com", "partner@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  pub,
	}
	store := allowedsigners.NewStore(entry)

	// The fixture must actually be trusted for BOTH identities before the
	// suppression, or the assertion below proves nothing.
	require.True(t, store.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"the multi-principal entry must grant trust before anything is suppressed")

	filtered := filterSuppressedPrincipals(store, map[string]bool{"vendor@example.com": true})

	assert.Empty(t, filtered.Entries(),
		"suppressing ONE principal drops the whole line, taking partner@example.com's grant with it")
	assert.False(t, filtered.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"the un-suppressed principal on the same line no longer grants trust either")
}

// unreadableFs makes exactly one path fail to open, so a test can distinguish
// "the file is not there" from "the file is there and I cannot read it".
type unreadableFs struct {
	afero.Fs
	openErr map[string]error
}

func (f *unreadableFs) Open(name string) (afero.File, error) {
	if err, ok := f.openErr[name]; ok {
		return nil, err
	}
	return f.Fs.Open(name)
}

// A revocation the operator wrote must hold even when the file recording it
// cannot be read. Untrusting takes effect immediately and stays in effect; a
// permissions problem is not a change of mind, and a trust root that quietly
// re-grants a key over an I/O error has reversed a human's "no" without
// telling anyone who could act on it.
//
// The two sides are the same fixture differing in one thing — whether the
// principal is revoked at all — so the negative assertion is proven capable of
// failing by the positive one directly above it.
func TestTrustRoot_UnreadableRevocationListDoesNotResurrectTrust(t *testing.T) {
	key := parseAuthorizedKey(t, ctxloomReleasePubkey)
	now := time.Now()

	newCfg := func(t *testing.T, revoked bool, readable bool) *Config {
		t.Helper()
		base := afero.NewMemMapFs()
		appDir := "/project/.ctxloom"
		require.NoError(t, base.MkdirAll(appDir, 0o755))
		path := paths.DistrustedSignersPath(appDir)
		if revoked {
			require.NoError(t, afero.WriteFile(base, path, []byte("ben+ctxloom@abbitt.me\n"), 0o600))
		}
		fs := base
		if !readable {
			fs = &unreadableFs{Fs: base, openErr: map[string]error{path: errors.New("permission denied")}}
		}
		cfg := &Config{appPaths: []string{appDir}}
		cfg.SetFS(fs)
		return cfg
	}

	// TRUSTED SIDE. Nothing revoked, list readable: the embedded release key
	// is trusted to publish. Without this, the assertions below could pass
	// against a trust root that trusts nothing at all.
	t.Run("a key nobody revoked is trusted", func(t *testing.T) {
		cfg := newCfg(t, false, true)
		assert.True(t, cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now).Trusted,
			"the embedded release key starts out trusted for publish")
	})

	// UNTRUSTED SIDE, the ordinary one: revoked and readable.
	t.Run("a revoked key is not trusted", func(t *testing.T) {
		cfg := newCfg(t, true, true)
		assert.False(t, cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now).Trusted,
			"a revoked principal's key must not be trusted")
	})

	// UNTRUSTED SIDE, the one that matters: revoked, and the record of the
	// revocation cannot be read.
	t.Run("a revoked key stays untrusted when the revocation list is unreadable", func(t *testing.T) {
		cfg := newCfg(t, true, false)
		assert.False(t, cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now).Trusted,
			"an unreadable revocation list must not re-grant a key the operator revoked")
	})
}
