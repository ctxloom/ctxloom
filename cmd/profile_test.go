// Tests for cmd/profile.go's extracted helpers. The cobra wrappers
// remain trivial composition (config + loader + delegate), so the
// testable surface is the mutation matrix in applyProfileMutations
// and the formatting in renderProfileList / renderProfileShow.
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// ptrString is a small helper for building profileMutations.SetDescription
// in test setup, since the field semantics require a pointer (nil = leave
// alone, non-nil = overwrite — including with the empty string).
func ptrString(s string) *string { return &s }

// =============================================================================
// renderProfileList
// =============================================================================

func TestRenderProfileList_HighlightsDefaults(t *testing.T) {
	list := []*profiles.Profile{
		{Name: "developer", Description: "Standard dev context", Bundles: []string{"a", "b"}},
		{Name: "reviewer", Parents: []string{"developer"}},
	}
	defaults := map[string]bool{"developer": true}

	var buf bytes.Buffer
	assert.NoError(t, renderProfileList(&buf, list, defaults))
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
	list := []*profiles.Profile{{Name: "minimal"}}

	var buf bytes.Buffer
	assert.NoError(t, renderProfileList(&buf, list, nil))
	out := buf.String()

	assert.Contains(t, out, "  minimal\n")
	assert.NotContains(t, out, "parents:")
	assert.NotContains(t, out, "bundles")
}

// =============================================================================
// renderProfileShow
// =============================================================================

func TestRenderProfileShow_AllSectionsPresent(t *testing.T) {
	p := &profiles.Profile{
		Name:             "developer",
		Path:             "/proj/.ctxloom/profiles/developer.yaml",
		Description:      "Standard dev context",
		Parents:          []string{"base"},
		Bundles:          []string{"alice/coding", "bob/standards"},
		Tags:             []string{"security"},
		Variables:        map[string]string{"LANG": "go"},
		ExcludeFragments: []string{"old-style"},
		ExcludePrompts:   []string{"legacy"},
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
	assert.Contains(t, out, "Excluded prompts:\n  - legacy")
	assert.Contains(t, out, "Excluded MCP servers:\n  - deprecated")
}

func TestRenderProfileShow_NonDefaultOmitsDefaultLine(t *testing.T) {
	p := &profiles.Profile{Name: "x", Path: "/p"}
	var buf bytes.Buffer
	assert.NoError(t, renderProfileShow(&buf, p, false))
	assert.NotContains(t, buf.String(), "Default:", "non-default profile must not show Default line")
}

func TestRenderProfileShow_EmptyOptionalSectionsSuppressed(t *testing.T) {
	p := &profiles.Profile{Name: "minimal", Path: "/p"}
	var buf bytes.Buffer
	assert.NoError(t, renderProfileShow(&buf, p, false))
	out := buf.String()

	for _, banned := range []string{"Description:", "Parents:", "Bundles:", "Tags:", "Variables:", "Excluded"} {
		assert.NotContains(t, out, banned, "empty profile must not emit %q", banned)
	}
}

// =============================================================================
// applyProfileMutations
// =============================================================================

func TestApplyProfileMutations_NoMutationsReturnsFalse(t *testing.T) {
	p := &profiles.Profile{Name: "x", Parents: []string{"existing"}}
	var buf bytes.Buffer
	modified := applyProfileMutations(p, profileMutations{}, iox.NewErrWriter(&buf))

	assert.False(t, modified, "empty mutations must return false")
	assert.Empty(t, buf.String(), "no mutations means no output")
	assert.Equal(t, []string{"existing"}, p.Parents, "profile must be untouched")
}

func TestApplyProfileMutations_SetDescriptionEmptyStringStillCounts(t *testing.T) {
	// Pointer-with-empty-value is a "clear the description" intent.
	// It must count as a modification even though the new value is "".
	p := &profiles.Profile{Description: "old"}
	var buf bytes.Buffer
	modified := applyProfileMutations(p, profileMutations{SetDescription: ptrString("")}, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	assert.Equal(t, "", p.Description)
}

func TestApplyProfileMutations_AddDedupes(t *testing.T) {
	p := &profiles.Profile{Parents: []string{"already-here"}}
	var buf bytes.Buffer
	modified := applyProfileMutations(p, profileMutations{
		AddParents: []string{"already-here", "fresh"},
	}, iox.NewErrWriter(&buf))

	assert.True(t, modified, "at least one fresh parent landed")
	assert.Equal(t, []string{"already-here", "fresh"}, p.Parents)

	out := buf.String()
	assert.Contains(t, out, "Parent already present: already-here")
	assert.Contains(t, out, "Added parent: fresh")
}

func TestApplyProfileMutations_RemoveMissing(t *testing.T) {
	p := &profiles.Profile{Bundles: []string{"keep"}}
	var buf bytes.Buffer
	modified := applyProfileMutations(p, profileMutations{
		RemoveBundles: []string{"never-was-there"},
	}, iox.NewErrWriter(&buf))

	assert.False(t, modified, "removing nothing must return false")
	assert.Equal(t, []string{"keep"}, p.Bundles)
	assert.Contains(t, buf.String(), "Bundle not found: never-was-there")
}

func TestApplyProfileMutations_AllExcludeSlotsRoundTrip(t *testing.T) {
	// Exercise every exclude_* slot end-to-end: add, then remove, then
	// confirm the no-op messages on a second pass. This pins the
	// per-slot message templates (each slot has its own four strings).
	cases := []struct {
		name             string
		field            func(*profiles.Profile) []string
		add, rm          func(profileMutations) profileMutations
		addedMsg, dupMsg string
		rmMsg, absentMsg string
	}{
		{
			name:  "exclude_fragments",
			field: func(p *profiles.Profile) []string { return p.ExcludeFragments },
			add: func(m profileMutations) profileMutations {
				m.AddExcludeFragments = []string{"f1"}
				return m
			},
			rm: func(m profileMutations) profileMutations {
				m.RemoveExcludeFragments = []string{"f1"}
				return m
			},
			addedMsg: "Added exclude fragment: f1", dupMsg: "Fragment already excluded: f1",
			rmMsg: "Removed exclude fragment: f1", absentMsg: "Fragment not excluded: f1",
		},
		{
			name:  "exclude_prompts",
			field: func(p *profiles.Profile) []string { return p.ExcludePrompts },
			add: func(m profileMutations) profileMutations {
				m.AddExcludePrompts = []string{"p1"}
				return m
			},
			rm: func(m profileMutations) profileMutations {
				m.RemoveExcludePrompts = []string{"p1"}
				return m
			},
			addedMsg: "Added exclude prompt: p1", dupMsg: "Prompt already excluded: p1",
			rmMsg: "Removed exclude prompt: p1", absentMsg: "Prompt not excluded: p1",
		},
		{
			name:  "exclude_mcp",
			field: func(p *profiles.Profile) []string { return p.ExcludeMCP },
			add: func(m profileMutations) profileMutations {
				m.AddExcludeMCP = []string{"m1"}
				return m
			},
			rm: func(m profileMutations) profileMutations {
				m.RemoveExcludeMCP = []string{"m1"}
				return m
			},
			addedMsg: "Added exclude MCP: m1", dupMsg: "MCP already excluded: m1",
			rmMsg: "Removed exclude MCP: m1", absentMsg: "MCP not excluded: m1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &profiles.Profile{}

			// Add fresh.
			var buf bytes.Buffer
			require.True(t, applyProfileMutations(p, tc.add(profileMutations{}), iox.NewErrWriter(&buf)))
			assert.Contains(t, buf.String(), tc.addedMsg)
			assert.Equal(t, []string{strings.SplitN(tc.addedMsg, ": ", 2)[1]}, tc.field(p))

			// Add duplicate -> no-op.
			buf.Reset()
			require.False(t, applyProfileMutations(p, tc.add(profileMutations{}), iox.NewErrWriter(&buf)))
			assert.Contains(t, buf.String(), tc.dupMsg)

			// Remove existing.
			buf.Reset()
			require.True(t, applyProfileMutations(p, tc.rm(profileMutations{}), iox.NewErrWriter(&buf)))
			assert.Contains(t, buf.String(), tc.rmMsg)
			assert.Empty(t, tc.field(p))

			// Remove absent -> no-op.
			buf.Reset()
			require.False(t, applyProfileMutations(p, tc.rm(profileMutations{}), iox.NewErrWriter(&buf)))
			assert.Contains(t, buf.String(), tc.absentMsg)
		})
	}
}

func TestApplyProfileMutations_MultipleSlotsTogether(t *testing.T) {
	// A single invocation can touch every slot — pin that the modified
	// flag goes true iff at least one slot saw a real change, and that
	// each slot's effect is independent.
	p := &profiles.Profile{
		Parents:          []string{"keep-parent"},
		Bundles:          []string{"keep-bundle"},
		ExcludeFragments: []string{},
	}
	mut := profileMutations{
		SetDescription:      ptrString("new-desc"),
		AddParents:          []string{"add-parent"},
		RemoveBundles:       []string{"keep-bundle"},
		AddExcludeFragments: []string{"new-excl"},
	}

	var buf bytes.Buffer
	modified := applyProfileMutations(p, mut, iox.NewErrWriter(&buf))

	assert.True(t, modified)
	assert.Equal(t, "new-desc", p.Description)
	assert.Equal(t, []string{"keep-parent", "add-parent"}, p.Parents)
	assert.Empty(t, p.Bundles, "keep-bundle was removed")
	assert.Equal(t, []string{"new-excl"}, p.ExcludeFragments)
}
