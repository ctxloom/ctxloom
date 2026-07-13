package memory

import (
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IsPlanFile matches project plan documents (the stamp-plan hook's regex:
// PLAN.md / *plan*.md anchored at start-or-slash / docs/*-plan.md). It is a
// distinct convention from the session-dir .plan.md files.
func TestIsPlanFile(t *testing.T) {
	for _, p := range []string{"PLAN.md", "current_plan.md", "plan-of-attack.md", "docs/auth-plan.md"} {
		assert.True(t, IsPlanFile(p), "%s should be a plan file", p)
	}
	for _, p := range []string{"README.md", "main.go", "docs/notes.md"} {
		assert.False(t, IsPlanFile(p), "%s should not be a plan file", p)
	}
}

func TestPlanFilesToBlocks(t *testing.T) {
	blocks := planFilesToBlocks([]agent.PlanFile{
		{Name: "arch", Content: "# arch"},
		{Name: "v1", Content: "# v1"},
	})
	require.Len(t, blocks, 2)
	assert.Equal(t, 1, blocks[0].Index)
	assert.Equal(t, PlanKindPlanFile, blocks[0].Kind)
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
			Index:     1,
			Kind:      PlanKindPlanFile,
			Label:     "ExitPlanMode",
			Timestamp: time.Date(2026, 5, 25, 14, 30, 0, 0, time.UTC),
			Content:   "1. step one\n2. step two with a `code` snippet",
		},
		{
			Index:   2,
			Kind:    PlanKindPlanFile,
			Label:   "plan.md",
			Content: "# Plan\n\n- thing",
		},
	}

	out := RenderPlans(blocks)

	assert.True(t, strings.HasPrefix(out, "## Preserved plans"))
	assert.Contains(t, out, "### Plan #1 — ExitPlanMode @ 2026-05-25 14:30")
	assert.Contains(t, out, "1. step one\n2. step two with a `code` snippet")
	assert.Contains(t, out, "### Plan #2 — plan.md")
	assert.Contains(t, out, "# Plan\n\n- thing")
}
