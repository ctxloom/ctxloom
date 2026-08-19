package operations

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// A bundle's fragment/command/mcp/hook/skill NAMES are bundle-authored — a
// YAML key or a filename the bundle's own author chose — never themselves
// ingested through remote.NormalizeRef before classify() concatenates them
// into a ref string. classify() DOES normalize that concatenated ref before
// the trust decision is taken against it, but the review display fields
// (ReviewItem.Name, ReviewItem.Ref) and the ReportVerdict/unaddressable-item
// diagnostics used to be built from the PRE-normalization locals, not the
// value the decision was actually made against.
//
// That gap mattered: NormalizeRef's whole point is that a ref shown to the
// human approving it — the entire purpose of `ctxloom review` — cannot carry
// CR/backspace/ESC and repaint what they see, and cannot carry a newline that
// would go on to forge the countersign preimage if ever concatenated into one
// downstream. A trust decision computed over the clean ref while the SCREEN
// showed the dirty one defeated exactly that property.
//
// This test pins the fix: a fragment named with an embedded control character
// must produce a ReviewItem whose Name and Ref are both clean, and whose Ref
// re-parses to the identical clean identity the original trust decision used.
func TestPendingReview_MaliciousItemNameCannotReachDisplay(t *testing.T) {
	const evilName = "solid\nEVIL-INJECTED-LINE"
	const cleanName = "solidEVIL-INJECTED-LINE"

	b := &bundles.Bundle{
		Version: "1.0",
		Fragments: map[string]bundles.BundleFragment{
			evilName: {Content: "body"},
		},
	}
	fx := newTrustFixture(t)
	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t, remoteSpec{name: "acme", url: trustRepo}),
		Loader:   reviewLoader(t, b),
		FS:       afero.NewMemMapFs(),
	})
	require.NoError(t, err)
	require.Len(t, res.Bundles, 1)
	require.Len(t, res.Bundles[0].Items, 1)

	item := res.Bundles[0].Items[0]
	assert.Equal(t, cleanName, item.Name, "the control character must not reach the review display name")
	assert.NotContains(t, item.Name, "\n")
	assert.NotContains(t, item.Ref, "\n")
	assert.Equal(t, seedItemRef(t, reviewSeedKey, "fragments/"+cleanName), item.Ref)

	// The displayed Ref must be the SAME identity the trust decision was made
	// against — round-tripping it through the parser the CLI's trust/reject
	// actions use must yield the clean name, not a second, different parse of
	// the raw bytes.
	ask, err := bundles.ParseItemAsk(item.Ref)
	require.NoError(t, err)
	assert.Equal(t, cleanName, ask.Item)
	assert.False(t, strings.ContainsAny(ask.Item, "\n\r"))
}
