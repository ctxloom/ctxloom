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

// journeyWidth is how many digits a journey number carries. FIXED width is the
// whole mechanism: numbers are spaced by 100 so a new journey can be inserted
// where it belongs, and only a fixed width makes a lexical sort (what `ls`, a
// file tree and a docs nav all do) agree with the numeric reading order.
// j000900 sorts before j001000; j900 would sort after it.
const journeyWidth = 6

// journeyStem matches a journey feature's number and name, and it is DELIBERATELY
// strict about the digit count — a name with the wrong width does not match, and
// the "every file is claimed" check below then fails it by name.
var journeyStem = regexp.MustCompile(`^j(\d{6})_[a-z0-9_]+\.feature$`)

// misWidened catches a FEATURE that looks like a journey but does not satisfy
// the strict stem, so a wrong digit count is reported rather than silently
// skipped. Scoped to .feature on purpose: a .doc.md companion is named after
// its feature and is not independently numbered, so holding it to the stem
// would flag every companion in the directory.
var misWidened = regexp.MustCompile(`^j\d+.*\.feature$`)

// TestJourneyNumbers_AreSpacedFixedWidthAndUnique pins the numbering the
// reading order depends on.
//
// Three properties, and only the third is about ordering:
//
//  1. FIXED WIDTH — six digits, always. This is what makes a lexical sort match
//     the reading order, so a directory listing tells the truth without anyone
//     consulting a manifest.
//  2. UNIQUE — two journeys on one number means a renumbering permutation
//     collided, which is silent: the suite still passes and every scenario still
//     runs.
//  3. GAPS ARE EXPECTED, not a defect. Numbers advance by 100 precisely so a
//     journey can later be inserted NEXT TO the one it belongs beside instead of
//     appended to the end. An earlier version of this test asserted contiguity,
//     which was right for a 1..N scheme and would now forbid the thing the
//     scheme exists to allow.
//
// What it still cannot check is whether a journey sits at the RIGHT position.
// That judgement is not derivable from the files: a journey inserted at
// j001250 passes this and may still be in the wrong place.
func TestJourneyNumbers_AreSpacedFixedWidthAndUnique(t *testing.T) {
	seen := map[int]string{}
	for _, dir := range []string{"features", filepath.Join("features", "journeys")} {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		for _, e := range entries {
			name := e.Name()
			m := journeyStem.FindStringSubmatch(name)
			if m == nil {
				if misWidened.MatchString(name) {
					t.Errorf("%s: a journey number must be exactly %d digits, zero-padded — "+
						"a different width sorts wrong, and the filename order IS the reading order",
						name, journeyWidth)
				}
				continue
			}
			n, err := strconv.Atoi(m[1])
			require.NoError(t, err)
			if prev, dup := seen[n]; dup {
				t.Errorf("journey number %d is used twice: %s and %s — a renumbering permutation collided",
					n, prev, name)
				continue
			}
			seen[n] = name
		}
	}
	require.NotEmpty(t, seen, "no journey features found; this test would pass vacuously")

	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)

	assert.Positive(t, nums[0], "journey numbers start above zero")
	for _, n := range nums {
		assert.LessOrEqual(t, len(strconv.Itoa(n)), journeyWidth,
			"%s: number exceeds the fixed width, so it can no longer be zero-padded into sort order", seen[n])
	}
}
