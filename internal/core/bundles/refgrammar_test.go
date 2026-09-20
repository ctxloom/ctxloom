package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Tests for fragment, prompt, and MCP server exclusion functionality.
// Exclusions accumulate through inheritance (cannot un-exclude).

func TestExclusions_QualifiedExclusionIsBundleScoped(t *testing.T) {
	// A qualified exclusion drops only its own bundle's fragment — same-named
	// fragments from other bundles survive (the repo-name-collision case).
	// Any reference spelling of the bundle matches via canonicalization.
	excluded := NewExclusions([]string{"dev#fragments/security-rules"})

	assert.True(t, excluded.Excludes("dev#fragments/security-rules"))
	assert.True(t, excluded.Excludes("ctxloom:local@bundles/dev#fragments/security-rules"))
	assert.False(t, excluded.Excludes("other#fragments/security-rules"))
	assert.False(t, excluded.Excludes("https://github.com/o/r@bundles/dev#fragments/security-rules"))
	assert.False(t, excluded.Excludes("security-rules"),
		"a bare name carries no origin; a qualified exclusion must not match it")
}

func TestExclusions_BareExclusionMatchesEveryBundle(t *testing.T) {
	// A bare exclusion is the name-wide kill switch: it matches its fragment
	// name wherever it comes from.
	excluded := NewExclusions([]string{"security-rules"})

	assert.True(t, excluded.Excludes("security-rules"))
	assert.True(t, excluded.Excludes("dev#fragments/security-rules"))
	assert.True(t, excluded.Excludes("https://github.com/o/r@bundles/tools#fragments/security-rules"))
	assert.False(t, excluded.Excludes("security"))
}
