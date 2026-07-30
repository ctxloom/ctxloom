package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// TestDropConflicted_LeavesCallerSliceIntact pins U085-F26: dropConflicted must
// build its own backing array. The in-place `pins[:0]` filter it used to run
// overwrote the caller's slice as it went, so the argument the caller still
// holds and the value returned aliased one array — a caller that kept the
// pre-filter slice (to report what was dropped, say) would silently read the
// filtered contents shifted into its first slots.
func TestDropConflicted_LeavesCallerSliceIntact(t *testing.T) {
	pins := []PinnedRef{
		{Identity: "a", Type: remote.ItemTypeBundle},
		{Identity: "b", Type: remote.ItemTypeBundle},
		{Identity: "c", Type: remote.ItemTypeBundle},
	}
	kept := dropConflicted(pins, []DependencyConflict{{Item: "a"}})

	keptIDs := make([]string, 0, len(kept))
	for _, p := range kept {
		keptIDs = append(keptIDs, p.Identity)
	}
	assert.Equal(t, []string{"b", "c"}, keptIDs, "the conflicted pin is dropped")

	callerIDs := make([]string, 0, len(pins))
	for _, p := range pins {
		callerIDs = append(callerIDs, p.Identity)
	}
	assert.Equal(t, []string{"a", "b", "c"}, callerIDs,
		"the caller's slice must be untouched — the filter owns its own array")
}

// TestFlattenDependencies_OnlyEverPinsBundles pins the construction that makes
// U085-F26's sibling claim (a non-bundle pin vanishing inside
// remote.Lockfile.AddEntry, U085-F28) unreachable: every PinnedRef the closure
// walk emits carries remote.ItemTypeBundle, which is also the only ItemType the
// remote package declares. AddEntry's documented non-bundle no-op therefore has
// no input that reaches it. If a second item type is ever added to the closure,
// this goes red and the AddEntry no-op has to be re-decided first.
func TestFlattenDependencies_OnlyEverPinsBundles(t *testing.T) {
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	pins, conflicts, unexpanded := FlattenDependencies(t.Context(), cfg, nil)
	require.Empty(t, conflicts)
	require.Empty(t, unexpanded)
	require.NotEmpty(t, pins, "the fixture closure must produce at least one pin")
	for _, p := range pins {
		assert.Equal(t, remote.ItemTypeBundle, p.Type,
			"pin %q must be a bundle — nothing else survives Lockfile.AddEntry", p.Identity)
	}
}
