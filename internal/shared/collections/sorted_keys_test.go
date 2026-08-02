package collections

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSortedKeys_Sorted covers the ordinary case: keys come back in ascending
// order regardless of map iteration order.
func TestSortedKeys_Sorted(t *testing.T) {
	got := SortedKeys(map[string]int{"charlie": 3, "alpha": 1, "bravo": 2})
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, got)
}

// TestSortedKeys_NamedStringKey pins that the ~string constraint really is
// honored for a named key type, not just plain string.
func TestSortedKeys_NamedStringKey(t *testing.T) {
	type harp string
	got := SortedKeys(map[harp]struct{}{"zulu": {}, "alpha": {}})
	assert.Equal(t, []harp{"alpha", "zulu"}, got)
}

// TestSortedKeys_EmptyAndNilYieldNonNilSlice pins the function's documented
// "nil in, empty (non-nil) slice out" contract, which is the ONLY reason this
// keeps a hand-written make/append loop instead of the shorter
// slices.Sorted(maps.Keys(m)): that returns a NIL slice for an empty map.
//
// The difference is observable, not cosmetic — a nil slice marshals to JSON
// `null` while an empty one marshals to `[]`, so switching to slices.Sorted
// would silently change every JSON payload built from an empty map.
func TestSortedKeys_EmptyAndNilYieldNonNilSlice(t *testing.T) {
	t.Run("empty map", func(t *testing.T) {
		got := SortedKeys(map[string]int{})
		require.NotNil(t, got, "an empty map must yield an EMPTY slice, never nil")
		assert.Empty(t, got)

		raw, err := json.Marshal(got)
		require.NoError(t, err)
		assert.JSONEq(t, `[]`, string(raw), "a nil slice would marshal to null and change the wire shape")
	})

	t.Run("nil map", func(t *testing.T) {
		var m map[string]int
		got := SortedKeys(m)
		require.NotNil(t, got, "a nil map must yield an EMPTY slice, never nil")
		assert.Empty(t, got)

		raw, err := json.Marshal(got)
		require.NoError(t, err)
		assert.JSONEq(t, `[]`, string(raw))
	})
}

// It might be argued this package holds two unrelated concerns because
// "SortedKeys is never called on a Set". That was true, and it is the
// load-bearing evidence for the cohesion claim, so it is worth exercising
// rather than merely asserting.
//
// A Set[T] IS a map[T]struct{}, so SortedKeys applies to one directly: reading a
// Set in a stable order is exactly what SortedKeys is for, and it is now what
// config's profile resolver does with the three set-backed Profile fields whose
// map-order reads used to reach an engine's settings file. This test pins the
// composition, so a split that separated the two would have to break it first.
func TestSortedKeys_OverASet(t *testing.T) {
	s := NewSetFrom("delta", "alpha", "charlie", "bravo")
	got := SortedKeys(s)

	if want := []string{"alpha", "bravo", "charlie", "delta"}; !slices.Equal(got, want) {
		t.Fatalf("SortedKeys over a Set = %v, want %v", got, want)
	}

	// And on the zero value, matching the Set doc's read-safety guarantee.
	var zero Set[string]
	if got := SortedKeys(zero); len(got) != 0 || got == nil {
		t.Fatalf("SortedKeys over a zero Set = %#v, want an empty non-nil slice", got)
	}
}
