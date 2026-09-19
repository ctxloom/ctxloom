package signing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCountersignPreimage_MatchesCountersignPayload is the entire contract of
// CountersignPreimage: routing a header through the three shape wrappers must
// produce exactly the bytes the raw framing function produces for that same
// header. It is the behaviour-neutrality proof for moving countersign.Store's
// signing, index-hash and verification paths off CountersignPayload — every
// existing record must keep verifying and keep landing on its existing
// filename.
//
// The table walks every axis a header has, including the shapes the wrappers do
// NOT name (a reject binding both a ref and a form; a ref-reject carrying a
// payload) so the fall-through arm is covered too — those are precisely where a
// careless dispatch would silently reframe.
func TestCountersignPreimage_MatchesCountersignPayload(t *testing.T) {
	payloads := [][]byte{nil, {}, []byte("body"), []byte("ref: x\nlen: 1\n\n")}
	refs := []string{"", "bundle#fragments/a", "https://example.com/r@bundles/b#mcp/pg"}
	forms := append(AttestationForms(), AttestNone)

	for _, a := range []Assertion{AssertionApprove, AssertionReject} {
		for _, ref := range refs {
			for _, form := range forms {
				for _, p := range payloads {
					h := CountersignHeader{Assertion: a, Ref: ref, Form: form}
					assert.Equal(t,
						string(CountersignPayload(h, p)),
						string(CountersignPreimage(h, p)),
						"header %+v payload %q", h, p)
				}
			}
		}
	}
}

// TestCountersignPreimage_RoutesThroughTheShapeWrappers pins that the three
// canonical shapes really do go through the trap-proof wrappers rather than
// only happening to agree with them — the point of the rewiring was to give
// those wrappers production callers.
func TestCountersignPreimage_RoutesThroughTheShapeWrappers(t *testing.T) {
	payload := []byte("body")

	approve := CountersignHeader{Assertion: AssertionApprove, Ref: "b#fragments/a", Form: AttestFragmentRaw}
	assert.Equal(t,
		string(ApproveCountersignPayload("b#fragments/a", AttestFragmentRaw, payload)),
		string(CountersignPreimage(approve, payload)))

	contentReject := CountersignHeader{Assertion: AssertionReject, Ref: "", Form: AttestFragmentRaw}
	assert.Equal(t,
		string(ContentRejectCountersignPayload(AttestFragmentRaw, payload)),
		string(CountersignPreimage(contentReject, payload)))

	refReject := CountersignHeader{Assertion: AssertionReject, Ref: "b#mcp/pg", Form: AttestNone}
	assert.Equal(t,
		string(RefRejectCountersignPayload("b#mcp/pg")),
		string(CountersignPreimage(refReject, nil)))
}
