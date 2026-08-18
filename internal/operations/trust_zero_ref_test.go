package operations

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestZeroRef_AddressesButIsInertAndUnreachable pins why a zero-valued
// trust.Ref producing a deterministic countersign-store address is not a
// silent no-op.
//
// U3 changed WHAT that address looks like: countersignRef used to compose
// CanonicalURL()+"|"+Key() unconditionally, so the zero Ref's well-formed-
// looking "|#/" needed its own harmlessness argument. It now bridges through
// Ref.AsBundleRef onto trust.BundleRef's stricter grammar, which refuses an
// empty bundle name (see bundleref_test.go, "minters refuse an empty name"),
// so the zero Ref no longer converts — countersignRef falls back to a
// %#v-of-the-Ref address instead (see countersignRef's doc for why that
// fallback can never collide with a real BundleRef.Identity()). The address
// is still DETERMINISTIC and still addresses nothing meaningful, so the same
// three properties still make it harmless, and each is asserted below rather
// than argued:
//
//  1. INERT at the countersignature seam. The empty kind derives no
//     attestation form, so nothing can be approved or content-rejected under
//     that address; Rejected's form loop does not execute at all.
//  2. WITHHELD by the decision function. Not rejected, not retracted, not
//     local, not builtin, no signer, not approvable — it falls through to the
//     terminal fail-closed default.
//  3. UNREACHABLE anyway. The zero Ref is produced by exactly one thing,
//     trust.ParseItemRef's failure arms, and it is produced only ALONGSIDE an
//     error that every caller checks before using the value.
func TestZeroRef_AddressesButIsInertAndUnreachable(t *testing.T) {
	zero := trust.Ref{}

	assert.Equal(t, "#/", zero.Key())
	assert.Empty(t, zero.CanonicalURL())
	assert.Equal(t, fmt.Sprintf("ctxloom+unaddressable:%#v", zero), countersignRef(zero))

	// 1. Nothing can be countersigned at that address.
	_, err := attestationFormFor(zero.Kind, signing.FormRaw)
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
	for _, bad := range []string{
		"",                    // no selector
		"#fragments/x",        // empty base
		"bundle#",             // empty selector
		"bundle#bogus/x",      // unknown kind directory
		"bundle#fragments/",   // empty name
		"https://x/y",         // a source ref with no selector at all
		"git@host:o/r#nope/x", // parseable source, unknown kind
	} {
		got, loadRef, version, perr := trust.ParseItemRef(bad)
		require.Errorf(t, perr, "%q must not parse", bad)
		assert.Equalf(t, trust.Ref{}, got, "%q must yield the zero Ref only alongside its error", bad)
		assert.Emptyf(t, loadRef, "%q", bad)
		assert.Emptyf(t, version, "%q", bad)
	}
}
