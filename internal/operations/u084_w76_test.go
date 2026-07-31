package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// U084-F13 (half one): sortContentInfos took an unvalidated sortBy and had no
// default branch, so any value other than "name"/"source" returned the input in
// whatever order the loader produced it — silently, with no warning. That is
// ctxloom's characteristic silent no-op wearing a sort's clothes: the caller
// asked for an ordering, got none, and was told nothing.
//
// The sibling ListProfiles (profiles.go) already had the correct shape — warn
// on an unknown sort_by and fall back to name — so this pin fixes the taxonomy
// in place rather than inventing a second one.
func TestSortContentInfos_UnknownSortByFallsBackToNameNotNoOp(t *testing.T) {
	// §11k hostility: the fixture must be in an order that is NOT already
	// name-ascending, otherwise a no-op sort would pass for the wrong reason.
	infos := []bundles.ContentInfo{
		{Name: "zebra", Source: "a"},
		{Name: "apple", Source: "b"},
		{Name: "mango", Source: "c"},
	}
	require.False(t, namesAreAscendingW76(infos),
		"fixture must start out of order, else a no-op sort passes vacuously")

	sortContentInfos(infos, "bogus-field", "asc")

	assert.Equal(t, []string{"apple", "mango", "zebra"}, namesOfW76(infos),
		"an unknown sort_by must still yield a deterministic ordering, not the input order")
}

// U084-F13 (half one, second face): the fallback must respect sort_order too —
// a "desc" request with an unknown field must not silently become ascending.
func TestSortContentInfos_UnknownSortByHonoursDescendingOrder(t *testing.T) {
	infos := []bundles.ContentInfo{
		{Name: "apple"},
		{Name: "zebra"},
		{Name: "mango"},
	}
	// §11k hostility: start ASCENDING-leaning so a no-op cannot look descending.
	require.NotEqual(t, []string{"zebra", "mango", "apple"}, namesOfW76(infos),
		"fixture must not already be in the asserted order")

	sortContentInfos(infos, "bogus-field", "desc")

	assert.Equal(t, []string{"zebra", "mango", "apple"}, namesOfW76(infos))
}

// U084-F13 (half two): containsTag folded the case of each TAG but not of the
// QUERY, so it carried an undocumented "caller must lowercase query"
// precondition. Every in-tree caller happens to honour it (ListFragments and
// SearchContent both pre-lower the query), which is exactly why it is a trap:
// the next caller inherits a silent false-negative with nothing to warn them.
//
// §11o: this defect is NOT observable at any public seam today — both public
// callers pre-lower — so this pin is deliberately at the UNIT altitude. The
// public-seam altitude is covered by the sort pins above; there is no
// public-seam pin for this half because there is no public seam that can reach
// it, and inventing one would mean removing the callers' own ToLower.
func TestContainsTag_QueryCaseIsNotACallerPrecondition(t *testing.T) {
	const query = "GoLang"
	// §11k hostility: the query must genuinely carry upper case, and the tag
	// must genuinely be lower case, or the assertion proves nothing.
	require.NotEqual(t, strings.ToLower(query), query, "query fixture must be mixed-case")
	tags := []string{"golang", "best-practices"}
	for _, tag := range tags {
		require.Equal(t, strings.ToLower(tag), tag, "tag fixture must be lower-case")
	}

	assert.True(t, containsTag(tags, query),
		"containsTag must fold the query's case itself rather than require the caller to")
}

// U084-F13 (half one) at the public seam: ListFragments is the exported entry
// point that hands sort_by straight through, so an unknown value must still
// come back ordered.
func TestListFragments_UnknownSortByStillReturnsOrderedResults(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	res, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		SortBy: "bogus-field",
		Loader: loader,
	})
	require.NoError(t, err)
	require.Equal(t, 4, res.Count)

	names := make([]string, 0, len(res.Fragments))
	for _, f := range res.Fragments {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"golang", "python", "security", "testing"}, names,
		"an unknown sort_by must fall back to name order, not the loader's order")
}

func namesOfW76(infos []bundles.ContentInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}

func namesAreAscendingW76(infos []bundles.ContentInfo) bool {
	for i := 1; i < len(infos); i++ {
		if strings.ToLower(infos[i-1].Name) > strings.ToLower(infos[i].Name) {
			return false
		}
	}
	return true
}
