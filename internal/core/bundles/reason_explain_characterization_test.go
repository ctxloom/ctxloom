package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestReason_Explain_PinsEverySentence pins the content-free sentence each
// Reason renders, with and without a verdict detail: it is the one place a
// Reason becomes user-facing words, and the default (any reason without a
// case of its own) must read as pending review.
func TestReason_Explain_PinsEverySentence(t *testing.T) {
	const review = "awaiting review — run 'ctxloom review'"
	cases := []struct {
		reason     Reason
		bare, with string // Explain(""), Explain("d")
	}{
		{ReasonUnset, review, review + " (d)"},
		{ReasonLocal, "allowed: " + ReasonLocal.String(), "allowed: " + ReasonLocal.String()},
		{ReasonCompanion, "allowed: " + ReasonCompanion.String(), "allowed: " + ReasonCompanion.String()},
		{ReasonTrustedSigner, "allowed: " + ReasonTrustedSigner.String(), "allowed: " + ReasonTrustedSigner.String()},
		{ReasonApproved, "allowed: " + ReasonApproved.String(), "allowed: " + ReasonApproved.String()},
		{ReasonStaleLocalSignature, "its signature no longer covers its bytes — re-sign it", "d"},
		{ReasonRejected, "rejected", "rejected"},
		{ReasonRetracted, "retracted by the publisher", "retracted by the publisher (d)"},
		{ReasonTampered, "its signature does not cover these bytes — withheld as tampered", "its signature does not cover these bytes — withheld as tampered (d)"},
		{ReasonUnsigned, review, review + " (d)"},
		{ReasonUntrustedSigner, "signed by a key this machine does not trust to publish — awaiting review — run 'ctxloom review'", "signed by a key this machine does not trust to publish — awaiting review — run 'ctxloom review'"},
		{ReasonPending, review, review + " (d)"},
		{ReasonUnaddressable, "its ref could not be parsed, so nothing could decide about it", "its ref could not be parsed, so nothing could decide about it"},
		{ReasonUnestablished, "it reached the gate without established provenance", "it reached the gate without established provenance"},
		{ReasonUngoverned, "it reached delivery with no authorizer, so nothing decided about it — this is a defect in ctxloom, not in the content", "it reached delivery with no authorizer, so nothing decided about it — this is a defect in ctxloom, not in the content"},
		{ReasonRecordsUnreadable, "its trust records could not be read, so nothing could be decided — repair them first", "its trust records could not be read, so nothing could be decided — repair them first (d)"},
		{Reason(255), review, review + " (d)"},
	}
	for _, tc := range cases {
		t.Run(tc.reason.String(), func(t *testing.T) {
			assert.Equal(t, tc.bare, tc.reason.Explain(""))
			assert.Equal(t, tc.with, tc.reason.Explain("d"))
		})
	}
}

// TestReason_RecordsUnreadable_IsListedUnderItsOwnName: a withhold because the
// trust records could not be read has its own wire spelling, distinct from
// "pending", so a listing stops sending the reader to review the item — and it
// stays LISTED, so a store fault never silently empties the review queue
// (operations' TestWeakHurt_PendingReview_UnreadableStore_ListingPath).
func TestReason_RecordsUnreadable_IsListedUnderItsOwnName(t *testing.T) {
	assert.Equal(t, "records-unreadable", ReasonRecordsUnreadable.String())
	assert.True(t, ReasonRecordsUnreadable.NeedsReview())
}
