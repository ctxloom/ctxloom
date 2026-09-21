package composite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The accumulator's rule at the unit level: a re-arrival under the SAME ref
// is dropped and reported as a duplicate of itself (the caller stays
// silent: two identical asks are unambiguous); a second copy of one item
// under ANOTHER ref is dropped and reports the ref that was kept, which is
// what the surface names.
func TestIngest_DropReportsTheKeptRef(t *testing.T) {
	in := newIngest()
	kept, dup := in.add(identityKey("ctxloom+local:dev#fragments/rules"), "RULES", "ctxloom+local:dev#fragments/rules")
	require.False(t, dup)
	assert.Equal(t, "ctxloom+local:dev#fragments/rules", kept)

	kept, dup = in.add(identityKey("ctxloom+local:dev#fragments/rules"), "RULES", "ctxloom+local:dev#fragments/rules")
	require.True(t, dup, "a re-ingest of the same ref is a duplicate")
	assert.Equal(t, "ctxloom+local:dev#fragments/rules", kept)

	kept, dup = in.add(identityKey("ctxloom+companion:dev#fragments/rules"), "RULES", "ctxloom+companion:dev#fragments/rules")
	require.True(t, dup, "the same item under another source is the same content")
	assert.Equal(t, "ctxloom+local:dev#fragments/rules", kept, "the first occurrence is the one kept")
	assert.Equal(t, "RULES", in.join(), "delivered once")
}

// identityKey pins the reduction the rule depends on: the companion and the
// local spellings of ONE item reduce to one key, and every distinction the
// rule must preserve survives the reduction. A ref outside the canonical
// grammar is used verbatim, so it can only ever match a byte-identical
// spelling — never a different one.
func TestIdentityKey_IsSourceAgnosticAndSelectorBearing(t *testing.T) {
	companion := identityKey("ctxloom+companion:isolation#fragments/isolation-axes")
	local := identityKey("ctxloom+local:isolation#fragments/isolation-axes")
	assert.Equal(t, companion, local, "the companion and local spellings of ONE item must reduce to one key")

	assert.NotEqual(t, companion, identityKey("ctxloom+companion:isolation#fragments/other-axes"), "a different item NAME is a different item")
	assert.NotEqual(t, companion, identityKey("ctxloom+companion:other-bundle#fragments/isolation-axes"), "a different BUNDLE is a different item")
	assert.NotEqual(t, companion, identityKey("ctxloom+companion:isolation#prompts/isolation-axes"), "a different item KIND is a different item")
	assert.Equal(t, "not a ref", identityKey("not a ref"))
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
