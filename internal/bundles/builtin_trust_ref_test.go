package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestBuiltinRead_ResolutionRefBare_TrustRefStillQualified is the REGRESSION
// GUARD for the slice that took the source class out of the resolution ref.
//
// The two strings are now deliberately different and must stay that way:
//
//   - the RESOLUTION ref (BundleRead.Ref) is the bare declared-independent
//     handle a caller addresses the bundle by — `isolation`, no source class,
//     because a bundle is addressed by what it declares and not by where it
//     sits;
//   - the TRUST ref (BundleRead.TrustSourceRef, i.e. Bundle.sourceRef) keeps
//     its `builtin:` qualification, because that string is what
//     trust.ParseItemRef reads to decide WHICH TRUST IDENTITY an item has.
//
// The trap this guards: the obvious way to un-qualify the resolution ref is to
// stop minting `builtin:<name>` in localFSReader.readBundle at all. That also
// un-qualifies the trust ref, because newRead stamps bundle.sourceRef from the
// resolution ref when it is empty. A bare trust ref falls through
// ParseItemRef's bare-token case to Ref{IsLocal}, which is a DIFFERENT trust
// identity from the Ref{IsBuiltin} the unconditional builtin-injection route
// gates on — so one item acquires two identities and a rejection recorded
// against either route leaves the other deliverable. That is the crispy-scoop
// gate bypass, arrived at from the opposite direction.
//
// So this asserts the divergence directly, on the real embedded builtin, at
// the reader that mints both strings.
func TestBuiltinRead_ResolutionRefBare_TrustRefStillQualified(t *testing.T) {
	const shared = "isolation" // the one bundle this binary actually embeds

	read, ok := NewLoader(NewBuiltinReader()).Read(shared)
	require.True(t, ok, "the embedded builtin must resolve by its bare name")

	assert.Equal(t, shared, read.Ref(),
		"a builtin's RESOLUTION ref carries no source class: it is addressed by what it declares")
	require.Equal(t, trust.BuiltinSourcePrefix+shared, read.TrustSourceRef(),
		"a builtin's TRUST ref must stay source-qualified even though its resolution ref no longer is")

	// The TYPED counterpart must name the identical identity, not merely a
	// string that happens to look right: BuiltinRef(shared) is the exact
	// minter localFSReader.readBundle calls to stamp it, so this pins that
	// the read carries a BundleRef equal to what that call site produces —
	// not a zero value that would silently make SourceRef() unaddressable.
	wantTyped, err := trust.BuiltinRef(shared)
	require.NoError(t, err)
	assert.Equal(t, wantTyped, read.SourceRef(),
		"a builtin's typed SourceRef must be the same BuiltinRef(shared) identity as its string TrustSourceRef")

	// The load-bearing half: what the qualification is FOR. An item ref built
	// from the trust ref must parse to the builtin identity — not to IsLocal,
	// which is where an unqualified string lands.
	tRef, _, _, err := trust.ParseItemRef(read.TrustSourceRef() + "#fragments/isolation-axes")
	require.NoError(t, err)
	assert.True(t, tRef.IsBuiltin,
		"a builtin item must gate as IsBuiltin; an unqualified trust ref silently becomes a second identity")
	assert.False(t, tRef.IsLocal, "a builtin must never be gated as project-local content")
	assert.Equal(t, trust.KindFragment, tRef.Kind)
	assert.Equal(t, "isolation-axes", tRef.Name)

	// And the item refs the loader itself hands the gate must agree with that
	// — asserting the reader's stamp alone would not catch a content path that
	// re-derived the source from the resolution ref.
	items, err := NewLoader(NewBuiltinReader()).ReadFragment(shared + "#fragments/isolation-axes")
	require.NoError(t, err)
	require.Len(t, items, 1, "the bare resolution ref must reach the builtin's fragment")
	wantItemRef, err := trust.BuiltinRef(shared)
	require.NoError(t, err)
	wantItemRefStr, err := wantItemRef.WithItem(trust.KindFragment, "isolation-axes")
	require.NoError(t, err)
	assert.Equal(t, wantItemRefStr.String(), items[0].TrustRef,
		"the item ref the gate sees must carry the builtin qualification, minted through the canonical bundle-reference grammar")
}
