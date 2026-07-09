// Tests for cmd/profile.go's extracted helpers. The cobra wrappers route
// create/modify/delete through internal/operations (covered by
// operations/profiles_test.go), so the remaining CLI-local testable surface
// is the formatting in renderProfileList / renderProfileShow.
package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// =============================================================================
// renderProfileList
// =============================================================================

func TestRenderProfileList_HighlightsDefaults(t *testing.T) {
	list := []operations.ProfileEntry{
		{Name: "developer", Description: "Standard dev context", Bundles: []string{"a", "b"}, Default: true},
		{Name: "reviewer", Parents: []string{"developer"}},
	}

	var buf bytes.Buffer
	assert.NoError(t, renderProfileList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "Profiles (2):")
	assert.Contains(t, out, "developer (default)", "default profile must be tagged")
	assert.NotContains(t, out, "reviewer (default)", "non-default profile must not be tagged")
	assert.Contains(t, out, "Standard dev context", "description renders on its own line")
	assert.Contains(t, out, "2 bundles", "bundle count rendered")
	assert.Contains(t, out, "parents: developer", "parents list rendered")
}

func TestRenderProfileList_EmptySectionsSuppressed(t *testing.T) {
	// A profile with no parents/bundles/description should produce only
	// the name line — no dangling "0 bundles" or empty description line.
	list := []operations.ProfileEntry{{Name: "minimal"}}

	var buf bytes.Buffer
	assert.NoError(t, renderProfileList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "  minimal\n")
	assert.NotContains(t, out, "parents:")
	assert.NotContains(t, out, "bundles")
}

// =============================================================================
// renderProfileShow
// =============================================================================

func TestRenderProfileShow_AllSectionsPresent(t *testing.T) {
	p := &operations.GetProfileResult{
		Name:             "developer",
		Path:             "/proj/.ctxloom/profiles/developer.yaml",
		Description:      "Standard dev context",
		Parents:          []string{"base"},
		Bundles:          []string{"alice/coding", "bob/standards"},
		Tags:             []string{"security"},
		Variables:        map[string]string{"LANG": "go"},
		ExcludeFragments: []string{"old-style"},
		ExcludeMCP:       []string{"deprecated"},
	}

	var buf bytes.Buffer
	assert.NoError(t, renderProfileShow(&buf, p, true))
	out := buf.String()

	assert.Contains(t, out, "Profile: developer")
	assert.Contains(t, out, "Path: /proj/.ctxloom/profiles/developer.yaml")
	assert.Contains(t, out, "Default: yes")
	assert.Contains(t, out, "Description: Standard dev context")
	assert.Contains(t, out, "Parents:\n  - base")
	assert.Contains(t, out, "Bundles:\n  - alice/coding")
	assert.Contains(t, out, "  - bob/standards")
	assert.Contains(t, out, "Tags:\n  - security")
	assert.Contains(t, out, "Variables:")
	assert.Contains(t, out, "LANG: go")
	assert.Contains(t, out, "Excluded fragments:\n  - old-style")
	assert.Contains(t, out, "Excluded MCP servers:\n  - deprecated")
}

func TestRenderProfileShow_NonDefaultOmitsDefaultLine(t *testing.T) {
	p := &operations.GetProfileResult{Name: "x", Path: "/p"}
	var buf bytes.Buffer
	assert.NoError(t, renderProfileShow(&buf, p, false))
	assert.NotContains(t, buf.String(), "Default:", "non-default profile must not show Default line")
}

func TestRenderProfileShow_EmptyOptionalSectionsSuppressed(t *testing.T) {
	p := &operations.GetProfileResult{Name: "minimal", Path: "/p"}
	var buf bytes.Buffer
	assert.NoError(t, renderProfileShow(&buf, p, false))
	out := buf.String()

	for _, banned := range []string{"Description:", "Parents:", "Bundles:", "Tags:", "Variables:", "Excluded"} {
		assert.NotContains(t, out, banned, "empty profile must not emit %q", banned)
	}
}
