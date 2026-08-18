package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
)

func pendingOf(bs ...operations.ReviewBundle) *operations.PendingReviewResult {
	total := 0
	for range bs {
		total++
	}
	return &operations.PendingReviewResult{Bundles: bs, Total: total}
}

// TestClassifyContentTrust_KeepsTheThreeCasesApart pins how this check reports
// the review state it is now derived from.
//
// Two earlier versions were wrong in the same direction. The first read the
// LISTING, where a BundleInfo carries only a signer string, so three facts
// rendered as one word — "UNSIGNED". The second read the publisher signature off
// the reads, which is a better fact and still the WRONG BAR: a countersignature
// covers the BYTES, so content a human reviewed and accepted is fully
// attributable whether or not the publisher ever signed it. Deriving from the
// signature reported accepted content as a problem.
//
// The distinctions are not cosmetic, because the remedies differ sharply. A
// signature that does not cover its bytes is indistinguishable from tampering
// and is fixed by NEITHER trusting a key NOR accepting the content. An untrusted
// key needs recognising, or the content reviewing. Never reviewed is not a
// defect the publisher must fix at all — accepting the bytes is a complete
// attestation on its own.
func TestClassifyContentTrust_KeepsTheThreeCasesApart(t *testing.T) {
	const marker = "M"

	t.Run("all clean reports ok", func(t *testing.T) {
		got := classifyContentTrust(marker, pendingOf())
		assert.Equal(t, doctorOK, got.Status)
	})

	t.Run("an invalid signature is named as tampering, not as unsigned", func(t *testing.T) {
		got := classifyContentTrust(marker, pendingOf(
			operations.ReviewBundle{Ref: "https://x/y@bundles/tampered", Publisher: bundles.ReasonTampered},
		))
		require.Equal(t, doctorWarn, got.Status)
		assert.Contains(t, got.Detail, "does NOT cover their bytes",
			"a signature that fails over its own bytes must be reported as such")
		assert.NotContains(t, got.Detail, "never reviewed",
			"an invalid signature is not merely unreviewed, and must not be filed as such")
		assert.NotContains(t, got.Detail, "signer trust",
			"trusting a key does not make a bytes/signature mismatch go away, so it must not be suggested")
		assert.NotContains(t, got.Detail, "ctxloom review",
			"nor may this case offer 'accept it yourself': accepting bytes whose own signature refutes them "+
				"is the one remedy that must never be suggested here")
	})

	t.Run("the fixable cases offer the local remedy, not only the remote one", func(t *testing.T) {
		got := classifyContentTrust(marker, pendingOf(
			operations.ReviewBundle{Ref: "https://x/y@bundles/untrusted", Publisher: bundles.ReasonUntrustedSigner},
			operations.ReviewBundle{Ref: "https://x/y@bundles/bare", Publisher: bundles.ReasonUnsigned},
		))
		require.Equal(t, doctorWarn, got.Status)
		assert.Contains(t, got.Detail, "ctxloom review",
			"the remediable cases must say the content can be reviewed and accepted locally; naming only "+
				"'ask the publisher' leaves the reader waiting on someone else for content they can accept themselves")
		assert.Contains(t, got.Detail, "review the content and accept it",
			"the untrusted-key case must also offer reviewing the content, not only trusting the key")
		assert.Contains(t, got.Detail, "re-pends if they change",
			"the acceptance must be described as bound to the reviewed bytes, or it reads as a standing "+
				"exemption for the source rather than a decision about content someone looked at")
	})

	t.Run("an untrusted key is separated from a missing signature", func(t *testing.T) {
		got := classifyContentTrust(marker, pendingOf(
			operations.ReviewBundle{Ref: "https://x/y@bundles/untrusted", Publisher: bundles.ReasonUntrustedSigner},
			operations.ReviewBundle{Ref: "https://x/y@bundles/bare", Publisher: bundles.ReasonUnsigned},
		))
		require.Equal(t, doctorWarn, got.Status)

		trustIdx := strings.Index(got.Detail, "does not trust")
		bareIdx := strings.Index(got.Detail, "never reviewed")
		require.NotEqual(t, -1, trustIdx, "the untrusted-key case must be reported")
		require.NotEqual(t, -1, bareIdx, "the never-reviewed case must be reported")

		assert.Contains(t, got.Detail, "untrusted")
		assert.Contains(t, got.Detail, "bare")
		assert.NotEqual(t, trustIdx, bareIdx,
			"the two must be reported as distinct groups: one is fixed by trusting the key, the other by "+
				"reviewing the bytes — and neither requires the publisher to do anything")
	})

	t.Run("all three at once are reported separately", func(t *testing.T) {
		got := classifyContentTrust(marker, pendingOf(
			operations.ReviewBundle{Ref: "https://x/y@bundles/a", Publisher: bundles.ReasonTampered},
			operations.ReviewBundle{Ref: "https://x/y@bundles/b", Publisher: bundles.ReasonUntrustedSigner},
			operations.ReviewBundle{Ref: "https://x/y@bundles/c", Publisher: bundles.ReasonUnsigned},
		))
		require.Equal(t, doctorWarn, got.Status)
		for _, want := range []string{"does NOT cover their bytes", "does not trust", "never reviewed"} {
			assert.Contains(t, got.Detail, want,
				"every distinct situation must survive into the report; collapsing them is what this check exists to stop")
		}
	})
}
