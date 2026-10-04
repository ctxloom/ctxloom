package composite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The accumulator's rule at the unit level: a re-arrival of the SAME
// source-qualified item with the same bytes is dropped and reports the ref
// that was kept; the same bytes under the same name from ANOTHER source are a
// different item and are delivered too.
func TestIngest_DropReportsTheKeptRef(t *testing.T) {
	in := newIngest()
	kept, dup := in.add(identityKey("ctxloom+local:dev#fragments/rules"), "RULES", "ctxloom+local:dev#fragments/rules")
	require.False(t, dup)
	assert.Equal(t, "ctxloom+local:dev#fragments/rules", kept)

	kept, dup = in.add(identityKey("ctxloom+local:dev#fragments/rules"), "RULES", "ctxloom+local:dev#fragments/rules")
	require.True(t, dup, "a re-ingest of the same ref is a duplicate")
	assert.Equal(t, "ctxloom+local:dev#fragments/rules", kept)

	kept, dup = in.add(identityKey("ctxloom+local:dev@abc123#fragments/rules"), "RULES", "ctxloom+local:dev@abc123#fragments/rules")
	require.True(t, dup, "the same item under another spelling is the same content")
	assert.Equal(t, "ctxloom+local:dev#fragments/rules", kept, "the first occurrence is the one kept")

	kept, dup = in.add(identityKey("ctxloom+companion:dev#fragments/rules"), "RULES", "ctxloom+companion:dev#fragments/rules")
	require.False(t, dup, "the same bytes from another source are another item")
	assert.Equal(t, "ctxloom+companion:dev#fragments/rules", kept)
	assert.Equal(t, "RULES"+contextSectionSeparator+"RULES", in.join(), "each source's item delivered once")
}

// identityKey pins the reduction the rule depends on: the item's
// source-qualified, version-less identity. Two sources' items of one name
// stay two keys; two spellings of one item (with and without a version)
// reduce to one. A ref outside the canonical grammar is used verbatim: it
// can only ever match a byte-identical spelling, never a different one.
func TestIdentityKey_IsSourceQualifiedAndVersionless(t *testing.T) {
	companion := identityKey("ctxloom+companion:isolation#fragments/isolation-axes")
	local := identityKey("ctxloom+local:isolation#fragments/isolation-axes")
	assert.NotEqual(t, companion, local, "the companion and local items of one name are two items")
	assert.Equal(t, identityKey("ctxloom+local:beta#fragments/tagged"), identityKey("ctxloom+local:beta@abc123#fragments/tagged"),
		"a version does not make another item")

	assert.NotEqual(t, companion, identityKey("ctxloom+companion:isolation#fragments/other-axes"), "a different item NAME is a different item")
	assert.NotEqual(t, companion, identityKey("ctxloom+companion:other-bundle#fragments/isolation-axes"), "a different BUNDLE is a different item")
	assert.NotEqual(t, companion, identityKey("ctxloom+companion:isolation#prompts/isolation-axes"), "a different item KIND is a different item")
	assert.Equal(t, "not a ref", identityKey("not a ref"))
}

// deliver voices FindingDuplicate only for a true duplicate — one
// source-qualified item arriving under two spellings — and never for two
// sources' items that happen to carry identical bytes: both of those reach
// the package.
func TestDeliver_DuplicateFindingOnlyForATrueDuplicate(t *testing.T) {
	frag := func(ref string) Item[Fragment] { return Item[Fragment]{Ref: ref, Value: Fragment{Name: ref, Body: "SAME"}} }

	a := &assembly{ingest: newIngest()}
	a.deliver(frag("ctxloom+local:dev#fragments/rules"), "ctxloom+local:dev#fragments/rules")
	a.deliver(frag("ctxloom+companion:dev#fragments/rules"), "ctxloom+companion:dev#fragments/rules")
	assert.Len(t, a.delivered, 2, "byte-identical items from two sources both arrive")
	assert.Empty(t, a.findings, "two distinct items are not a duplicate")

	a.deliver(frag("ctxloom+local:dev@abc123#fragments/rules"), "ctxloom+local:dev@abc123#fragments/rules")
	assert.Len(t, a.delivered, 2, "a true duplicate is assembled once")
	require.Len(t, a.findings, 1)
	assert.Equal(t, FindingDuplicate, a.findings[0].Kind)
	assert.Equal(t, "ctxloom+local:dev@abc123#fragments/rules", a.findings[0].Ref)
}

// The assembled STRING must not carry a "---" separator with nothing on one
// side of it.
func TestIngest_JoinOmitsBlankSections(t *testing.T) {
	in := newIngest()
	_, dup := in.add("b#fragment/blank", "   \n ", "b#fragments/blank")
	require.False(t, dup)
	_, dup = in.add("b#fragment/real", "REAL", "b#fragments/real")
	require.False(t, dup)
	assert.Equal(t, "REAL", in.join(), "a blank section must not be framed by a separator")
}
