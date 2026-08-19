package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestCatalogResolveAsk_CanonicalURIResolvesExactly proves ResolveAsk's arm 1:
// a canonical trust.BundleRef ask is resolved EXACTLY via LookupKey, and
// nothing about a name search is involved.
func TestCatalogResolveAsk_CanonicalURIResolvesExactly(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))
	cat := loader.Catalog()

	want, err := trust.LocalRef("kit")
	require.NoError(t, err)

	got, err := cat.ResolveAsk(want.String())
	require.NoError(t, err)
	assert.Equal(t, want.BundleIdentity(), got.BundleIdentity())
}

// TestCatalogResolveAsk_CanonicalURINotInCatalogRefusesNotFound proves that,
// unlike operations.ResolveItemAsk, Catalog.ResolveAsk ALWAYS consults the
// catalog: a well-formed canonical ref naming a bundle this catalog cannot
// see is refused, not returned as-is.
func TestCatalogResolveAsk_CanonicalURINotInCatalogRefusesNotFound(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	ghost, err := trust.LocalRef("no-such-bundle")
	require.NoError(t, err)

	_, err = cat.ResolveAsk(ghost.String())
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleNotFound)
}

// TestCatalogResolveAsk_RetiredSchemeMarkerRefusedNotDowngraded proves arm 2:
// a retired scheme marker is refused with ErrRetiredRefSpelling and never
// falls through to arm 3's name search — even though "builtin:isolation" is
// syntactically a perfectly good candidate bare name to search for.
func TestCatalogResolveAsk_RetiredSchemeMarkerRefusedNotDowngraded(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	for _, ask := range []string{
		"builtin:isolation",
		"ctxloom:local@bundles/kit",
		"ctxloom:companion@ltk",
		"git@github.com:acme/repo.git",
		"ssh://git@github.com/acme/repo",
	} {
		_, err := cat.ResolveAsk(ask)
		require.Error(t, err, "ask %q must be refused", ask)
		assert.ErrorIs(t, err, errs.ErrRetiredRefSpelling, "ask %q", ask)
		assert.NotErrorIs(t, err, errs.ErrBundleNotFound,
			"ask %q must be refused as a retired spelling, not downgraded to a name search", ask)
	}
}

// TestCatalogResolveAsk_BareNameResolvesUniquely proves arm 3's ordinary
// case: a bare name matching exactly one read resolves to that read's source.
func TestCatalogResolveAsk_BareNameResolvesUniquely(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	got, err := cat.ResolveAsk("kit")
	require.NoError(t, err)

	want, err := trust.LocalRef("kit")
	require.NoError(t, err)
	assert.Equal(t, want.BundleIdentity(), got.BundleIdentity())
}

// TestCatalogResolveAsk_BareNameNotFoundRefuses proves arm 3's zero-match
// case.
func TestCatalogResolveAsk_BareNameNotFoundRefuses(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	_, err := cat.ResolveAsk("no-such-bundle")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleNotFound)
}

// TestCatalogResolveAsk_AmbiguousBareNameRefuses proves arm 3's two-or-more
// case.
//
// The two reads share a display name but carry different canonical
// identities, which is exactly what two source classes shipping one declared
// name resolve to.
func TestCatalogResolveAsk_AmbiguousBareNameRefuses(t *testing.T) {
	localSrc, err := trust.LocalRef("isolation")
	require.NoError(t, err)
	builtinSrc, err := trust.BuiltinRef("isolation")
	require.NoError(t, err)

	localBundle := &Bundle{Name: "isolation", Version: "1.0.0"}
	localBundle.sourceRef = localSrc
	localBundle.sourceRefSet = true

	builtinBundle := &Bundle{Name: "isolation", Version: "1.0.0"}
	builtinBundle.sourceRef = builtinSrc
	builtinBundle.sourceRefSet = true

	unsigned := signatureFacts{signature: SignatureNone, signer: SignerNone}
	localRead := newRead("isolation", localBundle, ProvenanceProject, TrustCtxLocal, unsigned)
	builtinRead := newRead("isolation", builtinBundle, ProvenanceBuiltin, TrustCtxLocal, unsigned)

	cat := Catalog{reads: []BundleRead{localRead, builtinRead}}

	_, err = cat.ResolveAsk("isolation")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleAmbiguous)
}

// TestCatalogLookupKey_MatchesLookupRef proves LookupKey and LookupRef answer
// for the SAME identity — LookupKey is the drop-in for a caller that already
// extracted the key rather than holding the structured ref.
func TestCatalogLookupKey_MatchesLookupRef(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	want, err := trust.LocalRef("kit")
	require.NoError(t, err)

	byRef, ok := cat.LookupRef(want)
	require.True(t, ok)

	byKey, ok := cat.LookupKey(want.BundleIdentity())
	require.True(t, ok)
	assert.Equal(t, byRef.Key(), byKey.Key())
}

// TestBundleRead_KeyMatchesSourceRefBundleIdentity pins Key()'s contract: it
// is exactly SourceRef().BundleIdentity(), nothing derived from DisplayName
// (a label a bundle's declared name or a reader's composition order can move
// independently of its source identity).
func TestBundleRead_KeyMatchesSourceRefBundleIdentity(t *testing.T) {
	read := readOne(t, projectReaderOver(t, "kit.yaml", "version: 1.0.0\n"))
	assert.Equal(t, read.SourceRef().BundleIdentity(), read.Key())
	assert.NotEmpty(t, read.Key())
}

// TestCatalog_Lookup_CanonicalURIResolvesExactly proves Lookup's first arm: a
// canonical reference resolves by key alone, with no name search behind it.
func TestCatalog_Lookup_CanonicalURIResolvesExactly(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	local, err := trust.LocalRef("kit")
	require.NoError(t, err)

	read, err := cat.Lookup(local.String())
	require.NoError(t, err)
	assert.Equal(t, local.BundleIdentity(), read.Key())

	// The same grammar, a bundle this catalog does not hold: not found, and
	// NOT quietly answered by the bundle that happens to share the leaf name.
	builtin, err := trust.BuiltinRef("kit")
	require.NoError(t, err)
	_, err = cat.Lookup(builtin.String())
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleNotFound)
}

// TestCatalog_Lookup_RetiredSpellingRefusedNotDowngraded proves Lookup's
// retired-spelling arm: a spelling the grammar no longer accepts is refused as
// such, never re-read as a bare name that happens to look similar. "You typed
// a retired spelling" and "no such bundle" are different faults.
func TestCatalog_Lookup_RetiredSpellingRefusedNotDowngraded(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "isolation.yaml", "version: 1.0.0\n")).Catalog()

	_, err := cat.Lookup("builtin:isolation")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrRetiredRefSpelling)
	assert.NotErrorIs(t, err, errs.ErrBundleNotFound,
		"a retired spelling must not be downgraded to a name search that resolves to something else")
	assert.Contains(t, err.Error(), "ctxloom bundle trust --help",
		"a refusal a user cannot act on is a dead end; the refusal must say where the current grammar is")
}

// TestCatalog_Lookup_UnknownBareNameIsNotFound proves the zero-match arm stays
// distinguishable from both refusals above.
func TestCatalog_Lookup_UnknownBareNameIsNotFound(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()

	_, err := cat.Lookup("no-such-bundle")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrBundleNotFound)
	assert.NotErrorIs(t, err, errs.ErrRetiredRefSpelling)
	assert.NotErrorIs(t, err, errs.ErrBundleAmbiguous)
}
