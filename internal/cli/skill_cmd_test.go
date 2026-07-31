package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

func skillListCmdWithOutput() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

// TestPrintSkillList_GenuinelyEmpty is the case where no skills exist at all
// (no --bundle filter, or a filter that also matches the true empty state):
// the create-one hint is the right guidance.
func TestPrintSkillList_GenuinelyEmpty(t *testing.T) {
	cmd, buf := skillListCmdWithOutput()
	err := printSkillList(cmd, nil, "", 0)
	assert.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "No skills found.")
	assert.Contains(t, out, "Create one with: ctxloom skill create <bundle> <name>")
}

// TestPrintSkillList_BundleFilterExcludesEverything is U043-F19: --bundle
// naming a bundle that has no skills (or a typo) used to print the plain
// "No skills found." + create-a-skill hint even when skills exist in OTHER
// bundles — misleading, since skills do exist, just not under this filter.
// The message must name the filtered bundle and point at the unfiltered
// listing instead of suggesting nothing exists.
func TestPrintSkillList_BundleFilterExcludesEverything(t *testing.T) {
	cmd, buf := skillListCmdWithOutput()
	err := printSkillList(cmd, nil, "nonexistent-bundle", 3)
	assert.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, `No skills found in bundle "nonexistent-bundle".`)
	assert.Contains(t, out, "Skills exist in other bundles")
	assert.NotContains(t, out, "Create one with:",
		"the create-a-skill hint is wrong here — skills already exist, just not in this bundle")
}

// TestPrintSkillList_NonEmptyRendersEntries is the non-empty control case,
// pinning the existing grouped-by-bundle rendering survives the signature
// change unchanged.
func TestPrintSkillList_NonEmptyRendersEntries(t *testing.T) {
	cmd, buf := skillListCmdWithOutput()
	entries := []operations.SkillEntry{
		{Name: "code-reviewer", Source: "core"},
	}
	err := printSkillList(cmd, entries, "", 1)
	assert.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Skills (1):")
	assert.Contains(t, out, "core:")
	assert.Contains(t, out, "code-reviewer")
}

// `skill sync <bundle>#skills/` names a skill and then names nothing. Widening
// that to a whole-bundle sync rewrites the files: manifest of every skill the
// bundle ships — manifests the user never named, and whose rewrite is exactly
// what re-arms (or silently re-baselines) the install-time tamper check. The
// separator is an explicit narrowing request, so an empty name after it is a
// typo to refuse, never a broader default to assume.
func TestSplitSkillSyncRef(t *testing.T) {
	t.Run("bare bundle name selects every skill", func(t *testing.T) {
		b, n, err := splitSkillSyncRef("my-bundle")
		require.NoError(t, err)
		assert.Equal(t, "my-bundle", b)
		assert.Empty(t, n, "a bare bundle name is the documented whole-bundle sync")
	})

	t.Run("qualified ref selects exactly one skill", func(t *testing.T) {
		b, n, err := splitSkillSyncRef("my-bundle#skills/code-reviewer")
		require.NoError(t, err)
		assert.Equal(t, "my-bundle", b)
		assert.Equal(t, "code-reviewer", n)
	})

	for _, arg := range []string{"my-bundle#skills/", "my-bundle#skills/   "} {
		t.Run("empty name after the separator is refused: "+arg, func(t *testing.T) {
			_, _, err := splitSkillSyncRef(arg)
			require.Error(t, err, "an empty name after #skills/ must not widen to a whole-bundle sync")
			assert.Contains(t, err.Error(), "#skills/")
		})
	}
}
