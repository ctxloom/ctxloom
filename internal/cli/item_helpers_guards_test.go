package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U037-F01 (HIGH): editItem's only guard was `if newContent == cur.Content`, so
// an editor that returned an EMPTY buffer — a crash, `:w` on a truncated file, a
// wrapper that never wrote — silently replaced the item's whole body with
// nothing and printed "Updated fragment ...". editInEditor returns whatever
// bytes are on disk with no length check, and operations.SetItemContent
// validates only the kind and the item's existence.
func TestCheckEditedContent_RejectsAnEmptyBuffer(t *testing.T) {
	for _, edited := range []string{"", "   ", "\n\n\t"} {
		err := checkEditedContent(ItemTypeFragment, "go-testing", edited)
		require.Error(t, err, "an empty editor buffer must not overwrite the item (%q)", edited)
		assert.Contains(t, err.Error(), "go-testing", "the error names the item that was nearly destroyed")
	}
}

func TestCheckEditedContent_AcceptsRealContent(t *testing.T) {
	assert.NoError(t, checkEditedContent(ItemTypeFragment, "go-testing", "# Heading\n\nbody\n"))
}

// U037-F06: `fragment list --bundle nosuchbundle` printed "Fragments (0):" and
// exited 0 with no hint the bundle name was wrong, because the empty-result
// branch tested the UNFILTERED slice — non-empty whenever any fragment exists
// anywhere, so the helpful branch was unreachable for a bad filter.
//
// The discriminator: a bundle that exists and simply has no items of this kind
// is legitimately an empty listing; a bundle name that matches nothing is a
// typo, and telling them apart is the fix.
func TestCheckBundleFilter_UnknownBundleIsAnError(t *testing.T) {
	err := checkBundleFilter("nosuchbundle", []string{"my-tools", "ctxloom-default"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nosuchbundle")
}

func TestCheckBundleFilter_KnownButEmptyBundleIsFine(t *testing.T) {
	assert.NoError(t, checkBundleFilter("my-tools", []string{"my-tools"}))
	assert.NoError(t, checkBundleFilter("", []string{"my-tools"}))
	// Unknown bundle set (the lookup itself failed): do not invent a verdict.
	assert.NoError(t, checkBundleFilter("whatever", nil))
}
