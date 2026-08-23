package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/termsafe"
)

// The exploit body from the delicious-goatskin report: cursor-up plus
// erase-line, then publisher-controlled replacement text. Every path below is
// asserted against the BYTES that reach the writer, because the failure this
// test exists to catch is an assertion that merely "contains" the safe text
// while the live sequence survives next to it.
const exploitBody = "SAFE-LINE-ONE\nAFTER\x1b[1A\x1b[2KOVERWRITTEN-BY-PUBLISHER"

// forgingBundle is a pending bundle whose every publisher-authored string
// tries to rewrite the line above it: the bundle ref, the remote, the
// fingerprint, and the item's kind and name.
func forgingBundle() *operations.PendingReviewResult {
	return &operations.PendingReviewResult{
		Total:   1,
		Updates: 0,
		Bundles: []operations.ReviewBundle{{
			Ref:               "acme/evil\x1b[1A\x1b[2K  signer:  alice - a key you trust",
			Remote:            "origin\rsigner:  alice",
			Publisher:         bundles.ReasonUntrustedSigner,
			SignerFingerprint: "SHA256:aaaa\x1b[2K",
			Items: []operations.ReviewItem{{
				Bundle:         "acme/evil",
				Ref:            "acme/evil#fragments/f1",
				Kind:           "fragments",
				Name:           "f1\x1b[1A\x1b[2K",
				Status:         operations.ReviewStatusNew,
				CurrentContent: exploitBody,
			}},
		}},
	}
}

// The trust-decision forgery, closed. renderReviewList is the surface a human
// reads to decide which publisher to trust, and it renders the publisher's own
// strings; a single ESC byte surviving anywhere in this output is the whole
// defect.
func TestRenderReviewList_PublisherCannotForgeTheLineNamingThePublisher(t *testing.T) {
	var out bytes.Buffer

	renderReviewList(&out, forgingBundle())

	got := out.String()
	assert.NotContains(t, got, "\x1b", "no ESC byte may reach the terminal")
	assert.NotContains(t, got, "\r", "no carriage return may overwrite a line")

	// The escapes are shown, not deleted: the reviewer still learns that this
	// publisher put a cursor-up sequence in its own name.
	assert.Contains(t, got, "acme/evil^[[1A^[[2K  signer:  alice - a key you trust")
	assert.Contains(t, got, "(remote: origin^Msigner:  alice)")
	assert.Contains(t, got, "  new      fragments/f1^[[1A^[[2K\n")

	// And the ctxloom-authored line naming the publisher state survives intact
	// on its own line, which is what the forgery was aiming at.
	assert.Contains(t, got, "  signer:  untrusted key SHA256:aaaa^[[2K\n")
	assert.Contains(t, got, "Signed, but by a key this machine does not trust to publish.")
}

// The interactive walk's bundle header carries the same publisher strings as
// the listing, so it gets the same treatment.
func TestPrintReviewBundleHeader_RefAndRemoteAreInert(t *testing.T) {
	var out bytes.Buffer

	printReviewBundleHeader(&out, forgingBundle().Bundles[0])

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\r")
	assert.Contains(t, got, "acme/evil^[[1A^[[2K")
}

// The item header names the kind and the name the publisher chose, one line
// above the body. A newline inside a name would let it open a line of its own.
func TestPrintReviewItem_ItemNameCannotLeaveItsHeaderLine(t *testing.T) {
	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]
	item.Name = "f1\nNEW - this line is the publisher's"

	printReviewItem(&out, 1, 1, item)

	got := out.String()
	assert.Contains(t, got, "[1/1] fragments/f1^JNEW - this line is the publisher's (NEW)\n")
}

// The item BODY is what a reviewer is being asked to judge, so it is rendered
// byte-for-byte here — indented, inert, and with nothing dropped.
func TestPrintReviewItemBody_ExploitBodyRendersInert(t *testing.T) {
	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]

	printReviewItemBody(&out, item)

	assert.Equal(t, "  SAFE-LINE-ONE\n  AFTER^[[1A^[[2KOVERWRITTEN-BY-PUBLISHER\n", out.String())
}

// A diff is publisher bytes too — BOTH sides of it are — so the update path
// must not be the hole the full-content path closed.
func TestPrintReviewItemBody_DiffOfPublisherContentIsInert(t *testing.T) {
	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]
	item.Status = operations.ReviewStatusUpdate
	item.PreviousContent = "old line\n"
	item.CurrentContent = "new line\x1b[1A\x1b[2Kforged\n"

	printReviewItemBody(&out, item)

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Contains(t, got, "new line^[[1A^[[2Kforged")
}

// The alternate form is countersigned by the same approval, so it is shown —
// and it is publisher content on the same terminal.
func TestPrintReviewAlternateForm_IsInert(t *testing.T) {
	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]
	item.AlternateForm = "distilled"
	item.AlternateContent = exploitBody

	printReviewAlternateForm(&out, item)

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Contains(t, got, "  AFTER^[[1A^[[2KOVERWRITTEN-BY-PUBLISHER\n")
}

// An over-long body is capped, and the cap SAYS SO on the diagnostic channel.
// A review surface that silently swallowed the tail of what it is asking a
// human to judge would be a worse defect than the one being fixed.
func TestPrintReviewItemBody_OverLongBodyIsCappedAndAnnounced(t *testing.T) {
	var diag bytes.Buffer
	restore := clidiag.SetSink(&diag)
	defer restore()

	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]
	item.CurrentContent = strings.Repeat("x", termsafe.DefaultMaxBytes+5000)

	printReviewItemBody(&out, item)

	assert.Len(t, out.String(), termsafe.DefaultMaxBytes+len("  ")+len("\n"))
	assert.Contains(t, diag.String(), "acme/evil#fragments/f1")
	assert.Contains(t, diag.String(), "truncated to")
	assert.Contains(t, diag.String(), "--format json")
	// The notice is a message BODY: the diagnostic channel supplies the
	// "ctxloom: warning: " prefix, and carrying one here too made the live
	// repro read "ctxloom: warning: ctxloom: ...".
	assert.NotContains(t, diag.String(), "warning: ctxloom:")
}

// An empty body still reads as an empty body, not as a missing one: the
// placeholder review has always printed survives the seam change.
func TestPrintReviewItemBody_EmptyContentKeepsThePlaceholder(t *testing.T) {
	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]
	item.CurrentContent = ""

	printReviewItemBody(&out, item)

	assert.Equal(t, "  (empty)\n", out.String())
}

// `fragment show` / `command show` share one body. It is the path the report
// CONFIRMED exploitable, so it is asserted byte-for-byte too.
func TestPrintItemBody_ShowRendersPublisherContentInert(t *testing.T) {
	var out bytes.Buffer

	printItemBody(&out, "probe#fragments/example", "example\x1b[2K", exploitBody, false)

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Contains(t, got, "example^[[2K\n\n")
	assert.True(t, strings.HasSuffix(got, "AFTER^[[1A^[[2KOVERWRITTEN-BY-PUBLISHER\n"))
}

func TestPrintItemBody_DistilledMarkerStillPrints(t *testing.T) {
	var out bytes.Buffer

	printItemBody(&out, "probe#fragments/example", "example", "body\n", true)

	assert.Equal(t, "# (distilled version)\nexample\n\nbody\n", out.String())
}

// `bundle view <ref>#fragments/x` renders one item's body.
func TestWriteBundleViewText_ItemBodyIsInert(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writeBundleViewText(&out, "probe#fragments/x", "fragments/x", []byte(exploitBody)))

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Equal(t, "SAFE-LINE-ONE\nAFTER^[[1A^[[2KOVERWRITTEN-BY-PUBLISHER\n", got)
}

// `bundle view <bundle>` with no selector dumps the whole bundle DOCUMENT, and
// people redirect that to a file. It is still escaped — a terminal is still on
// the other end — but its blank lines are NOT collapsed, because handing back a
// document that is not the one on disk is a different bug from the one being
// fixed.
func TestWriteBundleViewText_WholeDocumentIsEscapedButNotCollapsed(t *testing.T) {
	var out bytes.Buffer
	doc := "name: probe\n\n\n\n\nfragments:\n  x: \x1b[2K\n"

	require.NoError(t, writeBundleViewText(&out, "probe", "", []byte(doc)))

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Equal(t, "name: probe\n\n\n\n\nfragments:\n  x: ^[[2K\n", got)
}
