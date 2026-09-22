package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A materialized surface is ctxloom out of the loop, so a withheld premised
// fragment cannot be pulled later. Emitting it as a skill package is what keeps
// progressive disclosure available to an engine ctxloom will not be present to
// serve — but only if the package is one the engine will actually LOAD.
func TestPremisedFragmentSkills(t *testing.T) {
	t.Run("the premise reaches SKILL.md frontmatter, not just the struct", func(t *testing.T) {
		got, err := PremisedFragmentSkills([]PremisedFragment{
			{Ref: "ctxloom-project#fragments/config-hierarchy", Premise: "You are changing a flag or a config key.", Content: "# Configuration precedence\n\nbody"},
		})
		require.NoError(t, err)
		require.Len(t, got, 1)

		require.Len(t, got[0].Files, 1)
		require.Equal(t, "SKILL.md", got[0].Files[0].RelPath, "path is relative to the skill's own directory")
		md := string(got[0].Files[0].Content)

		// SkillExport.Description is write-only — no engine reads it — so the
		// premise only reaches the model if it is in these bytes.
		require.Contains(t, md, "description: You are changing a flag or a config key.")
		require.Contains(t, md, "body", "the fragment body must be the skill body")

		// ParseSkillPackage rejects a package whose frontmatter name differs
		// from its directory basename, so these two must agree exactly.
		require.Contains(t, md, "name: "+got[0].Name)
		require.True(t, got[0].Enabled)
	})

	t.Run("the package name qualifies from the ref, never the bare fragment name", func(t *testing.T) {
		got, err := PremisedFragmentSkills([]PremisedFragment{
			{Ref: "code-review-structural#fragments/general", Premise: "p", Content: "c"},
		})
		require.NoError(t, err)
		// `general` alone is defined in seventeen bundles in the default corpus;
		// a flat package named for it would collide silently.
		require.NotEqual(t, "general", got[0].Name)
		require.Contains(t, got[0].Name, "code-review-structural")
		require.Contains(t, got[0].Name, "general")
		require.NotContains(t, got[0].Name, "#", "a package directory cannot carry a ref separator")
		require.NotContains(t, got[0].Name, "/")
	})

	t.Run("two refs mapping to one package name REFUSE rather than overwrite", func(t *testing.T) {
		// Distinct refs, identical after sanitising — the case that makes
		// "collision-free by construction" untrue in general.
		_, err := PremisedFragmentSkills([]PremisedFragment{
			{Ref: "a.b#fragments/x", Premise: "p", Content: "1"},
			{Ref: "a-b#fragments/x", Premise: "p", Content: "2"},
		})
		require.Error(t, err, "a silent overwrite here loses a fragment's payload")
		require.Contains(t, err.Error(), "a.b#fragments/x")
		require.Contains(t, err.Error(), "a-b#fragments/x")
	})

	t.Run("a multi-line premise stays a single frontmatter line", func(t *testing.T) {
		got, err := PremisedFragmentSkills([]PremisedFragment{
			{Ref: "b#fragments/f", Premise: "line one\nline two", Content: "c"},
		})
		require.NoError(t, err)
		md := string(got[0].Files[0].Content)
		front := md[:strings.Index(md, "\n---\n\n")]
		require.Contains(t, front, "description: line one line two")
		require.Equal(t, 1, strings.Count(front, "description:"), "a wrapped description would break the frontmatter")
	})

	t.Run("no withheld fragments yields no packages", func(t *testing.T) {
		got, err := PremisedFragmentSkills(nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})
}
