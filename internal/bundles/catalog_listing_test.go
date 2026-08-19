package bundles

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
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
	loader := NewLoader(projectReaderOver(t, "lang/go.yaml", "version: 1.0.0\n"))

	infos, err := loader.List()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "lang/go", infos[0].Name, "a listing must print the ref the user can type back")

	_, err = loader.Load(infos[0].Name)
	assert.NoError(t, err, "every name a listing prints must resolve")
}

// TestCatalogScoped_ExcludesBuiltinsAndKeepsAcquiredContent is the listing
// contract `bundle list` states in its own help: local plus pinned-remote
// content. A builtin ships inside the binary and is neither.
//
// The builtin here is the REAL embedded one (NewBuiltinReader over
// resources/builtin_bundles), not a fixture, so the assertion cannot pass by the
// builtin never having been there — the absence-satisfies-absence shape. The
// test proves it was present in the unscoped set first, then that scoping drops
// exactly it and keeps the remote and companion reads.
func TestCatalogScoped_ExcludesBuiltinsAndKeepsAcquiredContent(t *testing.T) {
	unsigned := signatureFacts{signature: SignatureNone, signer: SignerNone}
	acquired := staticReader{reads: []BundleRead{
		newRead("https://example.test/repo@bundles/pinned", &Bundle{Name: "https://example.test/repo@bundles/pinned", Version: "1.0.0"},
			ProvenanceRemote, TrustCtxRemote, unsigned),
		newRead(companionRefPrefix+"ltk", &Bundle{Name: companionRefPrefix + "ltk", Version: "1.0.0"},
			ProvenanceCompanion, TrustCtxLocal, unsigned),
	}}
	cat := Resolve(context.Background(),
		projectReaderOver(t, "local.yaml", "version: 1.0.0\n"),
		NewBuiltinReader(),
		acquired,
	)

	// The BARE name: a builtin is DISPLAYED by the name it declares. Its
	// canonical URI is what addresses it; the display name never did.
	const builtinRef = "isolation"
	require.Contains(t, namesOf(cat.Infos()), builtinRef,
		"guard: the embedded builtin must be in the unscoped set, or this test proves nothing")

	got := namesOf(cat.Scoped(ProvenanceProject, ProvenanceRemote, ProvenanceCompanion).Infos())

	assert.NotContains(t, got, builtinRef, "a builtin is not installed and must not be listed")
	assert.Contains(t, got, "local", "project content must still be listed")
	assert.Contains(t, got, "https://example.test/repo@bundles/pinned", "pinned remote content must still be listed")
	assert.Contains(t, got, companionRefPrefix+"ltk", "companion content must still be listed")
	assert.Len(t, got, 3, "scoping drops the builtin and nothing else")
}

// TestCatalogLookupRef_ResolvesByTypedSourceIdentity proves the typed
// counterpart to Lookup(ask): a caller holding a structured trust.BundleRef
// (LocalRef("kit"), the identity a real project bundle's SourceRef carries)
// resolves the same read Lookup("kit") does.
func TestCatalogLookupRef_ResolvesByTypedSourceIdentity(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))
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

func namesOf(infos []*BundleInfo) []string {
	out := make([]string, 0, len(infos))
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out
}
