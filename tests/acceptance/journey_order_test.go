//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// journeyStem matches a journey feature's number and name: j9_context_exhaustion.
var journeyStem = regexp.MustCompile(`^j(\d+)_[a-z0-9_]+\.feature$`)

// TestJourneyNumbers_AreContiguousAndUnique pins the property the 2026-08-08
// renumbering established: journey numbers are the READING ORDER, so they run
// 1..N with no gaps and no duplicates.
//
// This guards the operation, not the ordering. Reordering journeys means
// renumbering them, the mapping is a permutation, and a permutation applied
// carelessly collides — two journeys landing on one number, or a number vanishing
// mid-sequence. Both are silent: the suite still passes, every scenario still
// runs, and only a human reading the directory notices. The old corpus carried
// exactly this damage for months (13 was simply skipped, and nobody could say
// whether it meant a deleted journey or a typo).
//
// What it deliberately does NOT check is whether each journey sits at the RIGHT
// position — that judgement lives in journey-usability-order.plan.md and cannot
// be derived from the files. A future j27 appended for a journey belonging at
// position 5 passes this test and is still wrong.
func TestJourneyNumbers_AreContiguousAndUnique(t *testing.T) {
	seen := map[int]string{}
	for _, dir := range []string{"features", filepath.Join("features", "journeys")} {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		for _, e := range entries {
			m := journeyStem.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			n, err := strconv.Atoi(m[1])
			require.NoError(t, err)
			if prev, dup := seen[n]; dup {
				t.Errorf("journey number %d is used twice: %s and %s — a renumbering permutation collided", n, prev, e.Name())
				continue
			}
			seen[n] = e.Name()
		}
	}
	require.NotEmpty(t, seen, "no journey features found; this test would pass vacuously")

	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	assert.Equal(t, 1, nums[0], "journeys start at j1 — the first thing a reader meets")
	for i, n := range nums {
		if n != i+1 {
			t.Fatalf("journey numbers are not contiguous: expected j%d, found j%d (%s). "+
				"A gap means a journey was deleted or a renumbering dropped one; "+
				"the reading order is the numbering, so a hole in it is a hole in the order.",
				i+1, n, seen[n])
		}
	}
}
