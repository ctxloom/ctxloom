package operations

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

	// A HOSTILE PUBLISHER, not our own writer. content/convert refuses to write
	// a path with a newline in it — correctly, since SHA256SUMS cannot encode
	// one unescaped — so the malicious name has to be written straight into the
	// tree, which is exactly how it would arrive from a repository nobody here
	// controls.
	b := &bundles.Bundle{
		Version: "1.0.0",
		Fragments: map[string]bundles.BundleFragment{
			"decoy": {ItemBody: bundles.ItemBody{Content: "body"}},
		},
	}
	tree := seedHostileTree(t, reviewSeedKey, b, map[string][]byte{
		"fragments/" + evilName + ".md": []byte("body\n"),
	})
	fx := newTrustFixture(t)
	var warnings strings.Builder
	restore := clidiag.SetSink(&warnings)
	// The loader resolves its readers at construction, so the read — and the
	// diagnostic it emits — happens inside the capture window.
	loader := bundles.NewLoader(bundles.NewRepoFSReader(tree, reviewSeedKey,
		bundles.WithRepoURL(seedRepoURL(t, reviewSeedKey))))
	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t, remoteSpec{name: "acme", url: trustRepo}),
		Loader:   loader,
		FS:       afero.NewMemMapFs(),
	})
	restore()
	require.NoError(t, err)

	// TWO INDEPENDENT DEFENCES FIRE, and asserting both is what keeps this
	// honest — either alone would let the other rot unnoticed.
	//
	// FIRST, the name is sanitised where a reference is minted, and the forge
	// attempt is NAMED rather than quietly cleaned. This is the defence the
	// test was originally written for, and it is still reached.
	assert.Contains(t, warnings.String(), "control characters",
		"a reference carrying control characters must be reported, not silently cleaned")
	assert.Contains(t, warnings.String(), cleanName,
		"and the cleaned form is what any display would use")

	// SECOND, the tree never becomes reviewable content at all: SHA256SUMS
	// cannot encode such a path, so verification refuses the whole bundle. The
	// malicious name therefore cannot reach a display even if the sanitiser
	// above were removed.
	assert.Empty(t, res.Bundles, "a tree carrying an unencodable path must not reach the review surface")
	assert.NotContains(t, warnings.String(), "\nEVIL-INJECTED-LINE",
		"no raw control character may reach the operator's terminal")

	// The identity a trust decision is made against is the CLEAN one, and it
	// round-trips through the parser the CLI's trust/reject actions use. This
	// holds independently of any fixture, which is why it is asserted directly.
	ask, err := bundles.ParseItemAsk(seedItemRef(t, reviewSeedKey, "fragments/"+cleanName))
	require.NoError(t, err)
	assert.Equal(t, cleanName, ask.Item)
	assert.False(t, strings.ContainsAny(ask.Item, "\n\r"))
}
