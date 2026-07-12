package signing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyCountersignature_ApproveByTrustedKey(t *testing.T) {
	signer, pub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionApprove, Kind: KindFragments, Ref: "acme/tooling#fragments/x", Form: FormRaw}
	payload := []byte("the fragment body")

	sig, err := Sign(CountersignPayload(header, payload), signer, NamespaceApprove)
	require.NoError(t, err)

	root := rootWith("ben@abbitt.me", pub, NamespaceApprove)
	principal, ok := VerifyCountersignature(header, payload, sig, root, time.Now())
	assert.True(t, ok)
	assert.Equal(t, "ben@abbitt.me", principal)
}

// TRAP TEST (required by the S6 brief): a signature file whose HASH-INDEX
// would match (same header/payload) but whose SIGNATURE BODY is corrupted
// must resolve to "not verified" — never to an allow. Finding a candidate at
// the right index proves nothing; only a successful cryptographic verify
// does (spec §9.3, implementer trap #2).
func TestVerifyCountersignature_CorruptedSignatureBodyIsNotVerified(t *testing.T) {
	signer, pub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionApprove, Kind: KindFragments, Ref: "acme/tooling#fragments/x", Form: FormRaw}
	payload := []byte("the fragment body")

	sig, err := Sign(CountersignPayload(header, payload), signer, NamespaceApprove)
	require.NoError(t, err)

	// Corrupt the armored blob's body while leaving it superficially
	// well-formed (still unarmors) — simulating a truncated/bit-flipped file
	// at the CORRECT index-hash filename.
	corrupted := make([]byte, len(sig))
	copy(corrupted, sig)
	for i := len(corrupted) - 20; i < len(corrupted)-10; i++ {
		if corrupted[i] == 'A' {
			corrupted[i] = 'B'
		} else {
			corrupted[i] = 'A'
		}
	}

	root := rootWith("ben@abbitt.me", pub, NamespaceApprove)
	principal, ok := VerifyCountersignature(header, payload, corrupted, root, time.Now())
	assert.False(t, ok, "a corrupted signature body must never verify as an approval")
	assert.Empty(t, principal)
}

func TestVerifyCountersignature_UntrustedKeyIsNotVerified(t *testing.T) {
	signer, _ := newTestSigner(t)
	_, otherPub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionApprove, Kind: KindFragments, Ref: "acme/tooling#fragments/x", Form: FormRaw}
	payload := []byte("the fragment body")

	sig, err := Sign(CountersignPayload(header, payload), signer, NamespaceApprove)
	require.NoError(t, err)

	// The signature is perfectly valid — but by a key nobody trusts for
	// approve. A countersignature by an untrusted key is not an approval.
	root := rootWith("someone-else@example.com", otherPub, NamespaceApprove)
	principal, ok := VerifyCountersignature(header, payload, sig, root, time.Now())
	assert.False(t, ok)
	assert.Empty(t, principal)
}

func TestVerifyCountersignature_EditedBytesNoLongerVerify(t *testing.T) {
	signer, pub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionApprove, Kind: KindFragments, Ref: "acme/tooling#fragments/x", Form: FormRaw}
	payload := []byte("the original body")

	sig, err := Sign(CountersignPayload(header, payload), signer, NamespaceApprove)
	require.NoError(t, err)

	root := rootWith("ben@abbitt.me", pub, NamespaceApprove)
	// Same header, EDITED payload bytes — an upstream content change.
	principal, ok := VerifyCountersignature(header, []byte("the edited body"), sig, root, time.Now())
	assert.False(t, ok, "an approval of the old bytes must not verify over the new bytes")
	assert.Empty(t, principal)
}

// A reject signature must not be presentable as an approve, and vice versa —
// the namespace lives inside the signed blob, matching the header's Assertion.
func TestVerifyCountersignature_AssertionNamespaceMismatch(t *testing.T) {
	signer, pub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionApprove, Kind: KindFragments, Ref: "acme/tooling#fragments/x", Form: FormRaw}
	payload := []byte("body")

	// Signed under the REJECT namespace (wrong one for this header's assertion).
	sig, err := Sign(CountersignPayload(header, payload), signer, NamespaceReject)
	require.NoError(t, err)

	root := rootWith("ben@abbitt.me", pub, NamespaceApprove, NamespaceReject)
	principal, ok := VerifyCountersignature(header, payload, sig, root, time.Now())
	assert.False(t, ok)
	assert.Empty(t, principal)
}

func TestVerifyCountersignature_EmptyOrNilInputs(t *testing.T) {
	_, pub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionApprove, Kind: KindFragments, Ref: "x", Form: FormRaw}
	root := rootWith("ben@abbitt.me", pub, NamespaceApprove)

	_, ok := VerifyCountersignature(header, []byte("body"), nil, root, time.Now())
	assert.False(t, ok, "no signature bytes is never a verified countersignature")

	_, ok = VerifyCountersignature(header, []byte("body"), []byte("garbage"), root, time.Now())
	assert.False(t, ok, "an unparseable blob is not verified")

	_, ok = VerifyCountersignature(header, []byte("body"), []byte("garbage"), nil, time.Now())
	assert.False(t, ok, "a nil trust root trusts nothing")
}

func TestVerifyCountersignature_RefRejectEmptyPayload(t *testing.T) {
	signer, pub := newTestSigner(t)
	header := CountersignHeader{Assertion: AssertionReject, Kind: KindFragments, Ref: "acme/tooling#fragments/x", Form: FormNone}

	sig, err := Sign(CountersignPayload(header, nil), signer, NamespaceReject)
	require.NoError(t, err)

	root := rootWith("lead@team.example", pub, NamespaceReject)
	principal, ok := VerifyCountersignature(header, nil, sig, root, time.Now())
	assert.True(t, ok)
	assert.Equal(t, "lead@team.example", principal)
}
