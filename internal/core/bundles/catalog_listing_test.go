package bundles

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// TestCatalogInfos_ListsTheResolvableRefNotTheLeafName pins the listing's
// handle: what `bundle list` prints is what `bundle show`/`bundle remove` must
// accept, and only the read's ref resolves (Catalog.Lookup keys by it).
//
// A bundle below the top level is where this is decided. lang/go.yaml resolves
// as "lang/go" while its Bundle.Name is the leaf "go", so projecting Bundle.Name
// into the listing would print a handle that resolves to nothing — the same
// defect as listing a builtin nobody can remove, in a different place. The test
// asserts BOTH facts so the two names cannot quietly become one.
func TestCatalogInfos_ListsTheResolvableRefNotTheLeafName(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "lang/go", "version: 1.0.0\n"))

	infos, err := loader.List()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "lang/go", infos[0].Name, "a listing must print the ref the user can type back")

	_, err = loader.Load(infos[0].Name)
	assert.NoError(t, err, "every name a listing prints must resolve")
}

// TestCatalogLookupRef_ResolvesByTypedSourceIdentity proves the typed
// counterpart to Lookup(ask): a caller holding a structured trust.BundleRef
// (LocalRef("kit"), the identity a real project bundle's SourceRef carries)
// resolves the same read Lookup("kit") does.
func TestCatalogLookupRef_ResolvesByTypedSourceIdentity(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit", "version: 1.0.0\n"))
	cat := loader.Catalog()

	byName, err := cat.Lookup("kit")
	require.NoError(t, err, "Lookup(ask) must still resolve an unambiguous bare name")

	want, err := trust.LocalRef("kit")
	require.NoError(t, err)
	byTyped, ok := cat.LookupRef(want)
	require.True(t, ok, "LookupRef must resolve the typed identity Lookup(ask) resolves")

	assert.Equal(t, byName.Key(), byTyped.Key(), "both must resolve to the SAME read")

	// br's own item selector is ignored — LookupRef resolves the BUNDLE the
	// item lives in, not the item. A ref carrying "#fragments/x" must still
	// resolve the same bundle read as the bundle-level ref: this is what pins
	// BundleIdentity() (item-stripped) as the actual index key rather than
	// Identity() (item-carrying) — the two coincide for a bundle-level query,
	// so only an item-qualified query can catch a regression back to
	// Identity().
	itemQualified, err := want.WithItem(trust.KindFragment, "x")
	require.NoError(t, err)
	byItemQualified, ok := cat.LookupRef(itemQualified)
	require.True(t, ok, "an item-qualified BundleRef must still resolve its owning bundle")
	assert.Equal(t, byName.Key(), byItemQualified.Key())

	// An identity nothing was resolved under misses cleanly, no panic.
	other, err := trust.LocalRef("no-such-bundle")
	require.NoError(t, err)
	_, ok = cat.LookupRef(other)
	assert.False(t, ok, "an identity nothing was resolved under must miss")
}

// twoBundlesOneDisplayName resolves a catalog holding two bundles that show
// the SAME display name under DIFFERENT canonical URIs — the shape nothing
// shadows any more, and therefore the shape a listing has to be able to render
// distinguishably.
func twoBundlesOneDisplayName(t *testing.T) Catalog {
	t.Helper()
	localSrc, err := trust.LocalRef("isolation")
	require.NoError(t, err)
	companionSrc, err := trust.CompanionRef("isolation")
	require.NoError(t, err)

	localBundle := &Bundle{Name: "isolation", Version: "1.0.0"}
	localBundle.sourceRef = localSrc
	localBundle.sourceRefSet = true

	companionBundle := &Bundle{Name: "isolation", Version: "2.0.0"}
	companionBundle.sourceRef = companionSrc
	companionBundle.sourceRefSet = true

	unsigned := SignatureFacts{Signature: SignatureNone, Signer: SignerNone}
	return Resolve(context.Background(), nil, staticReader{reads: []BundleRead{
		NewRead("isolation", localBundle, ProvenanceProject, TrustCtxLocal, unsigned),
		NewRead("isolation", companionBundle, ProvenanceCompanion, TrustCtxLocal, unsigned),
	}})
}

// TestListingNames_ShowsTheURIWhenTwoRowsShareAName is the listing half of the
// ambiguity contract, and its point is the JOIN with the refusal: a user who
// asks for "isolation" is told it names more than one bundle and given the
// candidate URIs, so those exact URIs have to be findable in the listing or
// the remedy names a string the user cannot locate.
//
// It asserts the join directly — every URI the refusal names must appear in
// the labels the listing renders — rather than asserting a format twice, which
// would let the two drift apart while both tests stayed green.
func TestListingNames_ShowsTheURIWhenTwoRowsShareAName(t *testing.T) {
	cat := twoBundlesOneDisplayName(t)
	infos := cat.Infos()
	require.Len(t, infos, 2, "guard: the fixture must produce two rows, or this test proves nothing")
	require.Equal(t, infos[0].Name, infos[1].Name, "guard: both rows must show the SAME name")
	require.NotEqual(t, infos[0].Ref, infos[1].Ref, "guard: the two rows must be different bundles")

	_, err := cat.Lookup("isolation")
	require.Error(t, err, "a name two bundles share must be refused, not silently won")
	require.ErrorIs(t, err, errs.ErrBundleAmbiguous)

	labels := listingNamesFor(t, infos)
	require.Len(t, labels, 2)
	assert.NotEqual(t, labels[0], labels[1],
		"two rows a user has to choose between must not render identically")

	for _, info := range infos {
		assert.Contains(t, err.Error(), string(info.Ref),
			"the refusal must name every candidate's canonical URI")
		assert.Contains(t, strings.Join(labels, "\n"), string(info.Ref),
			"a URI the refusal tells the user to type must be findable in the listing")
	}
	// Listing order is by display name then canonical key (sortReads), so the
	// companion URI sorts ahead of the local one.
	assert.Equal(t, "isolation (ctxloom+companion:isolation)", labels[0])
	assert.Equal(t, "isolation (ctxloom+local:isolation)", labels[1])
}

// TestListingNames_LeavesAnUncontestedNameBare is the other half, and it is a
// bug of its own if it fails: disambiguating a name nothing collides with puts
// a URI in front of every reader for an ambiguity that is not there, and drains
// the parenthetical of the meaning "these two differ".
func TestListingNames_LeavesAnUncontestedNameBare(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit", "version: 1.0.0\n")).Catalog()
	infos := cat.Infos()
	require.Len(t, infos, 1, "guard: one row, or the assertion below is vacuous")
	require.NotEmpty(t, infos[0].Ref, "guard: the row must HAVE a URI it could have been disambiguated with")

	labels := listingNamesFor(t, infos)
	assert.Equal(t, []string{"kit"}, labels)

	_, err := cat.Lookup(labels[0])
	assert.NoError(t, err, "an uncontested row's label must still resolve")
}

// TestListingNames_LeavesARowWithNoURIBare covers the lockfile-only row
// (deleted upstream): it shares a name with a real bundle but has no canonical
// URI to be told apart BY, so an empty parenthetical would be noise claiming to
// be a handle.
func TestListingNames_LeavesARowWithNoURIBare(t *testing.T) {
	infos := []*BundleInfo{
		{Name: "isolation", Ref: "ctxloom+local:isolation"},
		{Name: "isolation", Deleted: true},
	}
	labels := listingNamesFor(t, infos)
	assert.Equal(t, "isolation (ctxloom+local:isolation)", labels[0])
	assert.Equal(t, "isolation", labels[1], "a row with no URI renders no parenthetical")
}

// listingNamesFor calls ListingNames and pins the one structural property
// every caller relies on: one label per info, in the same order, so a renderer
// may walk the two by index.
func listingNamesFor(t *testing.T, infos []*BundleInfo) []string {
	t.Helper()
	labels := ListingNames(infos)
	require.Len(t, labels, len(infos), "ListingNames must answer once per row, in order")
	return labels
}

// TestCatalogScoped_ViewDoesNotShareFailuresWithParent pins that Scoped hands
// out an INDEPENDENT view: Catalog is copied by value, and a value copy shares
// every map and slice's backing store, so each owned reference field Scoped
// does not rebuild is aliased between parent and view. reads, candidates and
// byKey are rebuilt; failures is the one that survived by reference.
//
// fs and warnOut are deliberately NOT covered and must not be copied: they
// are injected collaborators (a filesystem, a diagnostics sink), not state
// the Catalog owns, and every view is meant to read through the same
// filesystem and warn to the same writer as its parent.
//
// The test writes through the view in both directions — an insert and a
// delete — because a fresh-but-empty map would pass an insert-only check
// while still losing the parent's entries.
func TestCatalogScoped_ViewDoesNotShareFailuresWithParent(t *testing.T) {
	errBroken := errors.New("unparseable")
	parent := Catalog{failures: map[string]error{"broken": errBroken}}

	view := parent.Scoped(ProvenanceProject)
	view.failures["injected"] = errors.New("written through the view")
	delete(view.failures, "broken")

	assert.NotContains(t, parent.failures, "injected", "a write through the view must not appear in the parent")
	assert.Equal(t, map[string]error{"broken": errBroken}, parent.failures, "a delete through the view must not remove the parent's entry")
}
