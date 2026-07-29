package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

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
