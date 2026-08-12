package discover

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// List sorted by mtime with os.Stat called INSIDE the comparator. That is an
// INCONSISTENT comparator whenever a candidate's mtime changes between two
// comparisons — and discovery exists precisely for the case where ANOTHER live
// process owns the coordinator and rewrites its endpoint.json (on Serve()) as
// discovery runs. A comparator whose keys move underfoot violates sort.Slice's
// strict-weak-ordering contract and yields an arbitrary order; it also costs
// O(n log n) syscalls instead of n.
//
// The fix snapshots each candidate's mtime EXACTLY ONCE before sorting. This
// test observes that directly through the mtime seam: a candidate whose mtime
// is read more than once (i.e. inside the comparator) is the defect, and a
// re-read that returns a changed value is what corrupts the order.
func TestList_SnapshotsMtimeOncePerCandidate(t *testing.T) {
	home := testsupport.Isolate(t)

	const n = 16
	base := time.Unix(1_700_000_000, 0)
	want := make([]string, 0, n)
	desired := map[string]time.Time{}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("proj-%02d", i)
		writeEndpoint(t, home, key, fmt.Sprintf(`{"loopback_port":%d,"consumer_cred":%q}`, 1000+i, key), base)
		p := filepath.Join(home, ".ctxloom", "coord", key, "endpoint.json")
		// Higher index => NEWER, so the correct "most-recently-active first"
		// order is the REVERSE of path order — distinct from glob/tiebreak order,
		// so a passing order assertion proves the mtime snapshot actually drove
		// the sort.
		desired[p] = base.Add(time.Duration(i) * time.Second)
		want = append(want, key)
	}
	for l, r := 0, len(want)-1; l < r; l, r = l+1, r-1 {
		want[l], want[r] = want[r], want[l] // reverse: proj-15 … proj-00
	}

	var mu sync.Mutex
	counts := map[string]int{}
	orig := mtime
	t.Cleanup(func() { mtime = orig })
	mtime = func(path string) time.Time {
		mu.Lock()
		defer mu.Unlock()
		counts[path]++
		if counts[path] == 1 {
			return desired[path] // the snapshot value
		}
		// Any re-stat (the per-comparison-stat defect) observes a mutated file:
		// every re-read collapses to one instant, scrambling the order.
		return base.Add(-time.Hour)
	}

	eps, skipped := List()
	require.Empty(t, skipped)
	require.Len(t, eps, n)

	// Core red-first assertion: each candidate's mtime was read exactly once —
	// the snapshot — never re-stat inside the sort comparator.
	mu.Lock()
	defer mu.Unlock()
	for p, cnt := range counts {
		assert.Equalf(t, 1, cnt,
			"List must snapshot each candidate's mtime exactly once before sorting; %s was stat'd %d times "+
				"(a re-stat inside the comparator is the inconsistent-comparator defect)", filepath.Base(filepath.Dir(p)), cnt)
	}

	got := make([]string, len(eps))
	for i, ep := range eps {
		got[i] = ep.Cred
	}
	assert.Equal(t, want, got,
		"the order must follow the once-read mtime snapshot (newest first), not values re-read mid-sort")
}

// The tiebreak half: two coordinators sharing an mtime (1-second filesystem
// granularity, several started together) must resolve to a deterministic,
// reproducible order — the stable path tiebreak — rather than whatever an
// unstable sort happens to produce.
func TestList_EqualMtimes_TiebreakByPathIsDeterministic(t *testing.T) {
	home := testsupport.Isolate(t)
	shared := time.Unix(1_700_000_000, 0)
	writeEndpoint(t, home, "proj-b", `{"loopback_port":2,"consumer_cred":"b"}`, shared)
	writeEndpoint(t, home, "proj-a", `{"loopback_port":1,"consumer_cred":"a"}`, shared)
	writeEndpoint(t, home, "proj-c", `{"loopback_port":3,"consumer_cred":"c"}`, shared)

	eps, skipped := List()
	require.Empty(t, skipped)
	require.Len(t, eps, 3)
	assert.Equal(t, []string{"a", "b", "c"}, []string{eps[0].Cred, eps[1].Cred, eps[2].Cred},
		"endpoints sharing an mtime must sort deterministically by path ascending")
}
