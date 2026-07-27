package signing

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadoutEnvelope_RoundTrip_Unsigned proves an unsigned companion loadout
// — the common case for a companion with no build-time signing pipeline yet —
// decodes to the exact bundle bytes with an EMPTY verified signer (spec
// §10.1: unsigned is legal, ordinary, and takes the review path; it is never
// an error).
func TestLoadoutEnvelope_RoundTrip_Unsigned(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")

	raw, err := EncodeLoadoutEnvelope(bundle, nil, "")
	require.NoError(t, err)

	_, pub := newTestSigner(t)
	root := rootWith("bundles@ctxloom.dev", pub, NamespacePublish)

	decoded, signer, err := DecodeLoadoutEnvelope(raw, root, time.Now())
	require.NoError(t, err)
	assert.Equal(t, bundle, decoded, "the decoded bytes are the exact bundle bytes, verbatim")
	assert.Empty(t, signer, "unsigned loadout: empty verified signer")
}

// U134-F03: the loadout envelope had no payload floor on either side. An
// empty bundle encoded to a well-formed-looking envelope, and — even worse —
// an empty ENVELOPE (or one whose "bundle" field decodes to zero bytes)
// decoded successfully to (zero bytes, "", nil): a companion malfunctioning
// this way contributed nothing while looking exactly like a healthy,
// successfully-decoded probe. In production this is currently caught one
// layer up (companionloadout.Emit floors the encode side; bundles.ParseBundle
// floors the decode side), but the primitive itself — the one place a FUTURE
// caller (e.g. the org drop-in/MDM channel the trust model's Known Gap #7
// describes) would also go through — had no floor of its own.
func TestEncodeLoadoutEnvelope_RefusesEmptyBundle(t *testing.T) {
	_, err := EncodeLoadoutEnvelope(nil, nil, "")
	require.Error(t, err, "an empty bundle attests to nothing and must not encode as a well-formed envelope")
}

func TestDecodeLoadoutEnvelope_RefusesEnvelopeDecodingToEmptyBundle(t *testing.T) {
	raw, err := EncodeLoadoutEnvelope([]byte("x"), nil, "")
	require.NoError(t, err)
	// Tamper the encoded envelope down to an empty "bundle" field directly,
	// bypassing EncodeLoadoutEnvelope's own floor — this is exactly the shape
	// a malfunctioning or hostile companion (or a hand-built MDM payload)
	// could send over the wire.
	raw = []byte(`{"contract":"` + LoadoutContract + `","bundle":""}`)

	_, pub := newTestSigner(t)
	root := rootWith("bundles@ctxloom.dev", pub, NamespacePublish)

	_, signer, err := DecodeLoadoutEnvelope(raw, root, time.Now())
	require.Error(t, err, "a loadout envelope decoding to zero bundle bytes must be withheld, not silently accepted")
	assert.Empty(t, signer)
}

// TestLoadoutEnvelope_RoundTrip_SignedByTrustedKey proves a loadout signed by
// a key the caller trusts for the publish namespace resolves to that key's
// verified principal — the "signed by a trusted key -> allowed" contract
// test the task requires. This stands in for "the embedded ctxloom key": in
// production that key is compiled into config.TrustRoot(); here a fresh test
// key exercises the identical DecodeLoadoutEnvelope -> VerifyPublisher path.
func TestLoadoutEnvelope_RoundTrip_SignedByTrustedKey(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: hello\n")
	signer, pub := newTestSigner(t)

	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)

	raw, err := EncodeLoadoutEnvelope(bundle, sig, "ltk@example.com")
	require.NoError(t, err)

	root := rootWith("ltk@example.com", pub, NamespacePublish)
	decoded, verifiedSigner, err := DecodeLoadoutEnvelope(raw, root, time.Now())
	require.NoError(t, err)
	assert.Equal(t, bundle, decoded)
	assert.Equal(t, "ltk@example.com", verifiedSigner)
}

// TestLoadoutEnvelope_SignedByUntrustedKeyIsUnsigned proves a well-formed
// signature by a key NOT in the trust root is quietly unsigned-to-us — not an
// error, matching VerifyPublisher's own contract (trap #3: the advisory
// "signer" field is never trusted on its own).
func TestLoadoutEnvelope_SignedByUntrustedKeyIsUnsigned(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\n")
	signer, _ := newTestSigner(t)
	_, otherPub := newTestSigner(t)

	sig, err := Sign(bundle, signer, NamespacePublish)
	require.NoError(t, err)
	raw, err := EncodeLoadoutEnvelope(bundle, sig, "someone@example.com")
	require.NoError(t, err)

	root := rootWith("someone-else@example.com", otherPub, NamespacePublish)
	decoded, verifiedSigner, err := DecodeLoadoutEnvelope(raw, root, time.Now())
	require.NoError(t, err)
	assert.Equal(t, bundle, decoded)
	assert.Empty(t, verifiedSigner)
}

// TestLoadoutEnvelope_AdvisorySignerFieldIsNeverTrusted is trap #3, made
// concrete for THIS surface: an envelope can claim ANY signer string with no
// signature at all, and it must never be believed.
func TestLoadoutEnvelope_AdvisorySignerFieldIsNeverTrusted(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\n")
	// Forge the advisory signer claim onto an otherwise-unsigned envelope by
	// hand, simulating a hostile companion binary.
	forged := []byte(`{"contract":"ctxloom-loadout/1","bundle":"` + base64.StdEncoding.EncodeToString(bundle) + `","signer":"releases@ctxloom.dev"}`)

	_, pub := newTestSigner(t)
	root := rootWith("releases@ctxloom.dev", pub, NamespacePublish)
	_, verifiedSigner, err := DecodeLoadoutEnvelope(forged, root, time.Now())
	require.NoError(t, err)
	assert.Empty(t, verifiedSigner, "a claimed signer with NO signature must never be believed")
}

// TestLoadoutEnvelope_UnrecognizedContract proves a verifier fails closed on
// a contract string it does not recognize (spec §12: "must reject a payload
// whose contract string it does not recognize — never best-effort parse an
// unknown version").
func TestLoadoutEnvelope_UnrecognizedContract(t *testing.T) {
	raw := []byte(`{"contract":"ctxloom-loadout/2","bundle":"aGVsbG8="}`)
	_, pub := newTestSigner(t)
	root := rootWith("bundles@ctxloom.dev", pub, NamespacePublish)

	_, _, err := DecodeLoadoutEnvelope(raw, root, time.Now())
	assert.Error(t, err, "an unrecognized contract version must be withheld, never best-effort parsed")
}

// TestLoadoutEnvelope_Unparseable proves garbage input is withheld — returned
// as an error, never a panic and never treated as unsigned-but-usable. A
// hostile or buggy companion binary must not be able to crash ctxloom by
// printing garbage to stdout.
func TestLoadoutEnvelope_Unparseable(t *testing.T) {
	_, pub := newTestSigner(t)
	root := rootWith("bundles@ctxloom.dev", pub, NamespacePublish)

	for _, raw := range [][]byte{
		nil,
		[]byte(""),
		[]byte("not json at all"),
		[]byte(`{"contract":"ctxloom-loadout/1","bundle":"not-valid-base64!!!"}`),
		[]byte(`{"contract":"ctxloom-loadout/1"}`), // missing bundle field entirely -> empty base64 -> decodes to empty, not an error, but exercised for completeness
	} {
		bundleBytes, signer, err := DecodeLoadoutEnvelope(raw, root, time.Now())
		if err != nil {
			assert.Nil(t, bundleBytes)
			assert.Empty(t, signer)
			continue
		}
		// The only non-error case in this table is the missing-bundle-field
		// one, which base64-decodes to an empty (not nil) byte slice —
		// legitimate "empty bundle", not a crash.
		assert.Empty(t, bundleBytes)
	}
}

// TestLoadoutEnvelope_SignedByTrustedKeyButBytesTampered proves a TRUSTED
// key's signature that does not actually cover the delivered bytes is
// withheld (ErrSignatureTampered), never silently downgraded to unsigned —
// spec §10.2, and implementer trap #2's mirror for the loadout surface.
func TestLoadoutEnvelope_SignedByTrustedKeyButBytesTampered(t *testing.T) {
	original := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: original\n")
	signer, pub := newTestSigner(t)
	sig, err := Sign(original, signer, NamespacePublish)
	require.NoError(t, err)

	tampered := []byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: TAMPERED\n")
	raw, err := EncodeLoadoutEnvelope(tampered, sig, "bundles@ctxloom.dev")
	require.NoError(t, err)

	root := rootWith("bundles@ctxloom.dev", pub, NamespacePublish)
	bundleBytes, verifiedSigner, err := DecodeLoadoutEnvelope(raw, root, time.Now())
	require.Error(t, err)
	assert.Nil(t, bundleBytes)
	assert.Empty(t, verifiedSigner)
}
