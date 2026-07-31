package memory

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IsPlanFile matches project plan documents: a basename BEGINNING with "plan"
// or "current_plan", or the repo-relative path docs/<name>-plan.md.
func TestIsPlanFile(t *testing.T) {
	for _, p := range []string{"PLAN.md", "current_plan.md", "plan-of-attack.md", "docs/auth-plan.md"} {
		assert.True(t, IsPlanFile(p), "%s should be a plan file", p)
	}
	for _, p := range []string{"README.md", "main.go", "docs/notes.md"} {
		assert.False(t, IsPlanFile(p), "%s should not be a plan file", p)
	}
}

// TestIsPlanFile_UnmatchedShapes is characterization, not endorsement. Each
// path below is one a reader of the old doc comment ("*plan*.md,
// docs/*-plan.md") would expect to match, and none of them do. Naming them
// here keeps the gap visible so that closing it is a decision someone makes,
// rather than a regex quietly widening under a hook that rewrites files in
// the user's home directory.
func TestIsPlanFile_UnmatchedShapes(t *testing.T) {
	// The basename must BEGIN with "plan": "*plan*.md" overstates the regex.
	for _, p := range []string{"design-plan.md", "my_plan.md", "docs/the-plan-doc.md"} {
		assert.False(t, IsPlanFile(p), "%s: the regex anchors plan at the basename start", p)
	}

	// The docs leg is anchored at the start of the string rather than at a
	// path separator, and the stamp-plan hook is handed an ABSOLUTE file_path
	// by the host engine, so that leg never fires in production.
	assert.True(t, IsPlanFile("docs/auth-plan.md"))
	assert.False(t, IsPlanFile("/home/u/proj/docs/auth-plan.md"),
		"the docs leg matches only a repo-relative path")

	// The session plan-document convention, ~/.ctxloom/sessions/<harp>/<name>.plan.md.
	// internal/shared/plans reads a `sessions:` list out of these and its doc
	// names StampPlanFile as the writer of that list; this regex is what stands
	// between the hook and those files.
	for _, p := range []string{"v1-removal.plan.md", "/home/u/.ctxloom/sessions/vital-deaf-stunt/v1-removal.plan.md"} {
		assert.False(t, IsPlanFile(p),
			"%s: session plan documents are not matched today — see IsPlanFile's doc comment", p)
	}
}

func TestPlanFilesToBlocks(t *testing.T) {
	blocks := planFilesToBlocks([]agent.PlanFile{
		{Name: "arch", Content: "# arch"},
		{Name: "v1", Content: "# v1"},
	})
	require.Len(t, blocks, 2)
	assert.Equal(t, 1, blocks[0].Index)
	assert.Equal(t, "arch", blocks[0].Label)
	assert.Equal(t, "# arch", blocks[0].Content)
	assert.Equal(t, 2, blocks[1].Index)

	assert.Empty(t, planFilesToBlocks(nil))
}

func TestRenderPlans_Empty(t *testing.T) {
	assert.Equal(t, "", RenderPlans(nil))
	assert.Equal(t, "", RenderPlans([]PlanBlock{}))
}

func TestRenderPlans_RoundTripPreservesContent(t *testing.T) {
	blocks := []PlanBlock{
		{
			Index:   1,
			Label:   "ExitPlanMode",
			Content: "1. step one\n2. step two with a `code` snippet",
		},
		{
			Index:   2,
			Label:   "plan.md",
			Content: "# Plan\n\n- thing",
		},
	}

	out := RenderPlans(blocks)

	assert.True(t, strings.HasPrefix(out, "## Preserved plans"))
	assert.Contains(t, out, "### Plan #1 — ExitPlanMode")
	assert.Contains(t, out, "1. step one\n2. step two with a `code` snippet")
	assert.Contains(t, out, "### Plan #2 — plan.md")
	assert.Contains(t, out, "# Plan\n\n- thing")
}
