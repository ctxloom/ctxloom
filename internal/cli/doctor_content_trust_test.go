package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// TestClassifyContentTrust_KeepsTheThreeCasesApart pins the reason this check
// reads the READS rather than the listing.
//
// A BundleInfo carries only a signer string, so the previous version rendered
// three different facts as one word — "UNSIGNED" — and then guessed between them
// in a parenthetical that did not even mention the serious one. The readers had
// already established which was which, and why.
//
// The distinction is not cosmetic. A signature that does not cover its bytes is
// indistinguishable from tampering and is NOT fixed by trusting a key; an
// untrusted key usually just needs recognising; no signature at all is the
// publisher's omission. Telling a user to trust a key in response to the first
// would be advice that papers over the only case that might be an attack.
func TestClassifyContentTrust_KeepsTheThreeCasesApart(t *testing.T) {
	const marker = "M"

	t.Run("all clean reports ok", func(t *testing.T) {
		got := classifyContentTrust(marker, []contentTrustFact{
			{Ref: "https://x/y@bundles/a", Signature: bundles.SignatureValid, Signer: bundles.SignerTrusted},
		})
		assert.Equal(t, doctorOK, got.Status)
	})

	t.Run("an invalid signature is named as tampering, not as unsigned", func(t *testing.T) {
		got := classifyContentTrust(marker, []contentTrustFact{
			{
				Ref:       "https://x/y@bundles/tampered",
				Signature: bundles.SignatureInvalid,
				Signer:    bundles.SignerTrusted,
				Detail:    "digest mismatch",
			},
		})
		require.Equal(t, doctorWarn, got.Status)
		assert.Contains(t, got.Detail, "does NOT cover their bytes",
			"a signature that fails over its own bytes must be reported as such")
		assert.Contains(t, got.Detail, "digest mismatch",
			"the reader's own explanation must reach the user; it is the only thing that says WHY")
		assert.NotContains(t, got.Detail, "carrying no signature at all",
			"an invalid signature is not a missing one, and must not be filed as one")
		assert.NotContains(t, got.Detail, "signer trust",
			"trusting a key does not make a bytes/signature mismatch go away, so it must not be suggested")
	})

	t.Run("an untrusted key is separated from a missing signature", func(t *testing.T) {
		got := classifyContentTrust(marker, []contentTrustFact{
			{Ref: "https://x/y@bundles/untrusted", Signature: bundles.SignatureValid, Signer: bundles.SignerUntrusted},
			{Ref: "https://x/y@bundles/bare", Signature: bundles.SignatureNone, Signer: bundles.SignerNone},
		})
		require.Equal(t, doctorWarn, got.Status)

		trustIdx := strings.Index(got.Detail, "does not trust")
		bareIdx := strings.Index(got.Detail, "carrying no signature at all")
		require.NotEqual(t, -1, trustIdx, "the untrusted-key case must be reported")
		require.NotEqual(t, -1, bareIdx, "the no-signature case must be reported")

		assert.Contains(t, got.Detail, "untrusted")
		assert.Contains(t, got.Detail, "bare")
		assert.NotEqual(t, trustIdx, bareIdx,
			"the two must be reported as distinct groups: one is fixed by trusting a key, the other only by the publisher signing")
	})

	t.Run("all three at once are reported separately", func(t *testing.T) {
		got := classifyContentTrust(marker, []contentTrustFact{
			{Ref: "https://x/y@bundles/a", Signature: bundles.SignatureInvalid, Signer: bundles.SignerTrusted},
			{Ref: "https://x/y@bundles/b", Signature: bundles.SignatureValid, Signer: bundles.SignerUntrusted},
			{Ref: "https://x/y@bundles/c", Signature: bundles.SignatureNone, Signer: bundles.SignerNone},
		})
		require.Equal(t, doctorWarn, got.Status)
		for _, want := range []string{"does NOT cover their bytes", "does not trust", "carrying no signature at all"} {
			assert.Contains(t, got.Detail, want,
				"every distinct situation must survive into the report; collapsing them is what this check exists to stop")
		}
	})
}
