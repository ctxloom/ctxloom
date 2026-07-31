package memory

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// PlanBlock is a plan document preserved verbatim in the distilled output. The
// distiller re-attaches the full content as a trailing section so the summary
// LLM never has to paraphrase it. Plans come from the session's .plan.md files
// (served by the agent server), not from the transcript.
type PlanBlock struct {
	Index   int    // 1-based position
	Label   string // short handle: the plan file's base name
	Content string // verbatim plan markdown
}

// planFileRegex matches paths that look like project plan documents.
// Both legs are anchored: a basename match or a docs/<name>-plan.md path.
var planFileRegex = regexp.MustCompile(`(?i)(^|/)(current_)?plan[^/]*\.md$|^docs/[^/]+-plan\.md$`)

// IsPlanFile reports whether the given file path matches the project
// plan-document pattern. The stamp-plan hook and StampPlanFile share this
// single regex source of truth.
//
// What matches, stated to the letter rather than by example, because the two
// are not the same thing here:
//
//   - Any path whose BASE NAME begins with "plan" or "current_plan", case
//     insensitively, and ends in ".md" — PLAN.md, plan.md, current_plan.md,
//     plan-of-attack.md, plans.md.
//   - A path spelled exactly "docs/<something>-plan.md". That leg is anchored
//     at the start of the STRING, not at a path separator, so it matches only
//     a repository-relative path. The stamp-plan hook receives an absolute
//     file_path from the host engine, so this leg does not fire there.
//
// What does NOT match, and is easy to assume does: a basename with "plan"
// anywhere other than the start (design-plan.md, my_plan.md), and the session
// plan-document convention <name>.plan.md that lives under
// ~/.ctxloom/sessions/<harp>/. TestIsPlanFile pins both, and whether the
// second should be brought in is an open question rather than an oversight —
// internal/shared/plans describes this function's writer as the source of the
// `sessions:` list it reads out of those very files, while plans_test.go
// calls them a distinct convention.
func IsPlanFile(path string) bool {
	return planFileRegex.MatchString(path)
}

// planFilesToBlocks converts a session's plan documents (read from its ctxloom
// session directory) into ordered PlanBlocks for verbatim re-attachment.
func planFilesToBlocks(files []agent.PlanFile) []PlanBlock {
	blocks := make([]PlanBlock, 0, len(files))
	for i, f := range files {
		blocks = append(blocks, PlanBlock{
			Index:   i + 1,
			Label:   f.Name,
			Content: f.Content,
		})
	}
	return blocks
}

// RenderPlans formats the collected blocks as the trailing section of the
// distilled output. Returns the empty string when no blocks were found.
func RenderPlans(blocks []PlanBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Preserved plans\n\n")
	for _, block := range blocks {
		fmt.Fprintf(&b, "### Plan #%d — %s\n\n", block.Index, block.Label)
		b.WriteString(strings.TrimRight(block.Content, "\n"))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
