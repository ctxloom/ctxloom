package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveProfile_DiamondDoesNotBlowUp pins U049-F13: parent resolution
// cloned the visited set per parent and memoized nothing, so a diamond
// inheritance graph cost O(2^depth). maxProfileDepth bounds DEPTH, not total
// work, so a malformed but shallow-enough diamond could hang the process. The
// resolution must now be bounded (each shared ancestor merged once) AND still
// produce the correct merged result.
//
// The graph: at every level i two profiles (a_i, b_i) each inherit from BOTH
// profiles of the level below, so every node below the root is reachable by an
// exponential number of paths. resolveVisitHook aborts the run the moment the
// visit count proves the exponential blow-up returned, keeping the pre-fix
// (mutation) case fast instead of actually running 2^depth resolutions.
func TestResolveProfile_DiamondDoesNotBlowUp(t *testing.T) {
	const levels = 40
	const visitBudget = 100_000 // linear resolution stays far under this; 2^40 does not

	profs := map[string]Profile{
		"leafA": {Fragments: []FragmentRef{{Name: "fragA"}}},
		"leafB": {Fragments: []FragmentRef{{Name: "fragB"}}},
	}
	prevA, prevB := "leafA", "leafB"
	for i := 0; i < levels; i++ {
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		profs[a] = Profile{Parents: []string{prevA, prevB}}
		profs[b] = Profile{Parents: []string{prevA, prevB}}
		prevA, prevB = a, b
	}
	profs["root"] = Profile{Parents: []string{prevA, prevB}}

	var visits int
	resolveVisitHook = func(string) {
		visits++
		if visits > visitBudget {
			panic(fmt.Sprintf("runaway profile resolution: %d visits — memoization absent (O(2^depth))", visits))
		}
	}
	defer func() { resolveVisitHook = nil }()

	var resolved *Profile
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("profile resolution blew up: %v", r)
			}
		}()
		p, err := ResolveProfile(profs, "root")
		require.NoError(t, err)
		resolved = p
	}()

	// Correctness: both leaves' fragments survive the diamond merge.
	names := map[string]bool{}
	for _, f := range resolved.Fragments {
		names[f.Name] = true
	}
	assert.True(t, names["fragA"], "leaf A fragment must survive the merge")
	assert.True(t, names["fragB"], "leaf B fragment must survive the merge")
	assert.Less(t, visits, visitBudget, "resolution must be bounded, not exponential (visits=%d)", visits)
}

// TestResolveProfile_MemoDoesNotMaskCycles guards that the memoization did not
// weaken the cycle guard: a genuine inheritance cycle must still be rejected.
func TestResolveProfile_MemoDoesNotMaskCycles(t *testing.T) {
	profs := map[string]Profile{
		"x": {Parents: []string{"y"}},
		"y": {Parents: []string{"x"}},
	}
	_, err := ResolveProfile(profs, "x")
	require.Error(t, err, "a circular inheritance chain must still be caught")
}
