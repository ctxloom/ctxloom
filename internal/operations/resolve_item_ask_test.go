package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestResolveItemAsk_CanonicalURIResolvesWithoutConsultingCatalog pins the
// property SetBlacklist's "written even when the item cannot be resolved"
// rests on: a canonical item ref resolves against an EMPTY catalog — the
// bundle that carried the item is gone (uninstalled, retracted, never pulled
// on this machine) — and ResolveItemAsk still answers it, rather than
// refusing because the catalog cannot see the bundle.
func TestResolveItemAsk_CanonicalURIResolvesWithoutConsultingCatalog(t *testing.T) {
	var empty bundles.Catalog // zero value: sees nothing

	br, err := ResolveItemAsk(empty, "ctxloom+local:kit#fragments/x")
	require.NoError(t, err)
	assert.Equal(t, "kit", br.Bundle)
	assert.Equal(t, trust.KindFragment, br.Kind)
	assert.Equal(t, "x", br.Item)
}

// TestResolveItemAsk_BareNameDelegatesToCatalogAndAttachesSelector proves the
// non-canonical path: the bundle half resolves through the catalog (bare
// name -> Catalog.ResolveAsk), and the "#<kind>/<item>" selector is attached
// to whatever that returns.
func TestResolveItemAsk_BareNameDelegatesToCatalogAndAttachesSelector(t *testing.T) {
	cat := seedLoader(t, map[string]*bundles.Bundle{"kit": {Version: "1.0.0"}}).Catalog()

	br, err := ResolveItemAsk(cat, "kit#fragments/x")
	require.NoError(t, err)
	assert.Equal(t, "kit", br.Bundle)
	assert.Equal(t, trust.KindFragment, br.Kind)
	assert.Equal(t, "x", br.Item)
	assert.Equal(t, trust.ClassLocal, br.Class)
}

// TestResolveItemAsk_BareNameOfUnknownBundleFailsClosed proves the bare-name
// path DOES consult the catalog (unlike the canonical-URI path): a bundle
// name the catalog cannot see is refused, not silently minted as a local ref.
func TestResolveItemAsk_BareNameOfUnknownBundleFailsClosed(t *testing.T) {
	cat := seedLoader(t, map[string]*bundles.Bundle{"kit": {Version: "1.0.0"}}).Catalog()

	_, err := ResolveItemAsk(cat, "no-such-bundle#fragments/x")
	require.Error(t, err)
}

// TestResolveItemAsk_MissingSelectorErrors proves a non-canonical ask with no
// "#<kind>/<name>" selector is refused rather than silently treated as a
// bundle-level ask.
func TestResolveItemAsk_MissingSelectorErrors(t *testing.T) {
	cat := seedLoader(t, map[string]*bundles.Bundle{"kit": {Version: "1.0.0"}}).Catalog()

	_, err := ResolveItemAsk(cat, "kit")
	require.Error(t, err)
}

// TestResolveItemAsk_InvalidSelectorKindErrors proves an unrecognized kind
// directory in the selector is refused.
func TestResolveItemAsk_InvalidSelectorKindErrors(t *testing.T) {
	cat := seedLoader(t, map[string]*bundles.Bundle{"kit": {Version: "1.0.0"}}).Catalog()

	_, err := ResolveItemAsk(cat, "kit#nonsense/x")
	require.Error(t, err)
}
