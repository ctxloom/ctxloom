package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// An item name is bundle-authored: a fragment/command/mcp key straight out of
// the bundle's own YAML, never itself put through remote.NormalizeRef before
// reaching a display line. The trust DECISION (TrustStamper.ForRef, via
// trust.Ref.Key) always normalizes its own copy, but that says nothing
// about what gets printed — printBundleItemTrust used to interpolate the raw
// name straight into the terminal line it writes for `bundle show -i`.
//
// This pins the fix: a control character in the name must never reach the
// printed line, even though the (unrelated) trust label may still come back
// Pending/withheld because no such bundle exists.
func TestPrintBundleItemTrust_StripsControlCharactersFromName(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	stamper := operations.NewTrustStamper(cfg)

	var out bytes.Buffer
	printBundleItemTrust(&out, stamper, "somebundle", trust.KindFragment, "solid\nEVIL-INJECTED-LINE")

	assert.NotContains(t, out.String(), "\n\n", "the printed line must not carry an embedded newline from the name")
	assert.Contains(t, out.String(), "fragments/solidEVIL-INJECTED-LINE:")
	// Exactly one newline: the trailing one printBundleItemTrust itself emits.
	assert.Equal(t, 1, strings.Count(out.String(), "\n"))
}

// listItemRows is the `fragment list` / `command list` read path (item_list.go).
// Its row() closure builds Name/Bundle/Ref straight from the operations
// projection's Name/Source fields, which are just as bundle-authored as the
// name printBundleItemTrust receives, and reached the listing unnormalized for
// the same reason. There is no seam to inject a malicious ListFragments
// result here, so this is covered at the unit level by exercising row's
// stripping helper indirectly is impractical; instead see
// TestPrintBundleItemTrust_StripsControlCharactersFromName above for the
// shared mechanism (remote.StripRefControlChars) and
// internal/operations.TestPendingReview_MaliciousItemNameCannotReachDisplay
// for the equivalent end-to-end proof on the review surface.
