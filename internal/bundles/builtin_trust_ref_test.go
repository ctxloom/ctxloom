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
//   - the TRUST ref (BundleRead.SourceRef, i.e. Bundle.sourceRef) keeps its
//     builtin qualification (trust.ClassBuiltin), because that is what a
//     structured item ref reads to decide WHICH TRUST IDENTITY an item has.
//
// The trap this guards: the obvious way to un-qualify the resolution ref is to
// stop minting trust.BuiltinRef(<name>) in localFSReader.readBundle at all.
// That also un-qualifies the trust ref, because newRead stamps
// bundle.sourceRef from the resolution ref when it is unset. An unqualified
// trust ref mints as trust.ClassLocal, a DIFFERENT trust identity from the
// ClassBuiltin the unconditional builtin-injection route gates on — so one
// item acquires two identities and a rejection recorded against either route
// leaves the other deliverable. That is the crispy-scoop gate bypass, arrived
// at from the opposite direction.
//
// So this asserts the divergence directly, on the real embedded builtin, at
// the reader that mints both refs.
func TestBuiltinRead_ResolutionRefBare_TrustRefStillQualified(t *testing.T) {
	const shared = "isolation" // the one bundle this binary actually embeds

	read, err := NewLoader(NewBuiltinReader()).Read(shared)
	require.NoError(t, err, "the embedded builtin must resolve by its bare name")

	assert.Equal(t, shared, read.DisplayName(),
		"a builtin's DISPLAY name carries no source class: it is shown as what it declares")

	// The TYPED source must name the builtin identity, not merely a value that
	// happens to look right: BuiltinRef(shared) is the exact minter
	// localFSReader.readBundle calls to stamp it, so this pins that the read
	// carries a BundleRef equal to what that call site produces — not a zero
	// value that would silently make SourceRef() unaddressable.
	wantTyped, err := trust.BuiltinRef(shared)
	require.NoError(t, err)
	assert.Equal(t, wantTyped, read.SourceRef(),
		"a builtin's typed SourceRef must be the BuiltinRef(shared) identity")
	assert.Equal(t, trust.ClassBuiltin, read.SourceRef().Class,
		"a builtin's TRUST ref must stay source-qualified even though its resolution ref no longer is")
	assert.NotEqual(t, trust.ClassLocal, read.SourceRef().Class,
		"an unqualified trust ref silently becomes a second identity")

	// The load-bearing half: what the qualification is FOR. An item ref built
	// from the trust ref must carry the builtin class in its canonical
	// rendering — not local, which is where an unqualified source lands.
	itemRefStr, err := ItemRefFor(read.SourceRef(), trust.KindFragment, "isolation-axes")
	require.NoError(t, err)
	itemRef, err := trust.ParseBundleRef(itemRefStr)
	require.NoError(t, err)
	assert.Equal(t, trust.ClassBuiltin, itemRef.Class,
		"a builtin item must gate under ClassBuiltin; an unqualified trust ref silently becomes a second identity")
	assert.Equal(t, trust.KindFragment, itemRef.Kind)
	assert.Equal(t, "isolation-axes", itemRef.Item)

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
