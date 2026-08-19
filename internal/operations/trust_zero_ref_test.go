package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestZeroRef_HasNoAddressAndIsInert pins that a zero-valued trust.Ref has NO
// countersign-store address at all, and that nothing downstream can act as
// though it had one.
//
// CountersignRef bridges through Ref.AsBundleRef onto trust.BundleRef's
// stricter grammar, which refuses an empty bundle name (see bundleref_test.go,
// "minters refuse an empty name"), so the zero Ref does not convert and
// CountersignRef REFUSES it. No placeholder address is minted: an address is
// what a human's countersignature is recorded against, and one that addresses
// nothing would record a decision nobody could ever have made.
//
// The three properties that make the zero Ref harmless are asserted below
// rather than argued:
//
//  1. UNADDRESSABLE at the countersignature seam — no address, and the empty
//     kind derives no attestation form either, so nothing can be approved or
//     content-rejected under it.
//  2. WITHHELD by the decision function. Not rejected, not retracted, not
//     local, not builtin, no signer, not approvable — it falls through to the
//     terminal fail-closed default.
//  3. UNREACHABLE anyway. The zero Ref is produced by exactly one thing,
//     the ask boundary's failure arms, and it is produced only ALONGSIDE an
//     error that every caller checks before using the value.
func TestZeroRef_HasNoAddressAndIsInert(t *testing.T) {
	zero := trust.Ref{}

	assert.Equal(t, "#/", zero.Key())
	assert.Empty(t, zero.CanonicalURL())
	addr, err := CountersignRef(zero)
	require.Error(t, err, "the zero Ref must have no countersign-store address")
	assert.Empty(t, addr, "a refusal must yield no address, not a stand-in for one")

	// 1. Nothing can be countersigned at that address.
	_, err = attestationFormFor(zero.Kind, signing.FormRaw)
	assert.Error(t, err, "the empty kind must derive no attestation form")
	assert.Empty(t, attestationFormsFor(zero.Kind),
		"an empty kind offering a form would let a content rejection — or an approval — key off '|#/'")

	// 2. The decision function withholds it.
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:        zero,
		Posture:    postureCtxOf(zero),
		Provenance: postureProvOf(zero),
		Payload:    []byte("bytes"),
		Form:       string(signing.FormRaw),
		Records:    fakeRecords{},
		Retraction: fakeRetraction{},
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourcePending, res.Source)

	// 3. Every producer of a zero Ref hands it back only with an error.
	cat := seedLoader(t, map[string]*bundles.Bundle{"bundle": {Version: "1.0.0"}}).Catalog()
	for _, bad := range []string{
		"",                    // no selector
		"#fragments/x",        // empty base
		"bundle#",             // empty selector
		"bundle#bogus/x",      // unknown kind directory
		"bundle#fragments/",   // empty name
		"https://x/y",         // a source ref with no selector at all
		"git@host:o/r#nope/x", // scheme-marked source, unknown kind
	} {
		got, perr := ResolveItemAsk(cat, bad)
		require.Errorf(t, perr, "%q must not resolve", bad)
		assert.Equalf(t, trust.BundleRef{}, got, "%q must yield the zero ref only alongside its error", bad)
		assert.Equalf(t, trust.Ref{}, trust.RefFromBundleRef(got), "%q", bad)
	}
}
