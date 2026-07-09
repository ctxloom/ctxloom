package memory

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// PlanKind identifies what surface produced a preserved plan block.
type PlanKind string

const (
	PlanKindExitPlanMode PlanKind = "exit_plan_mode"
	PlanKindTodoWrite    PlanKind = "todo_write"
	PlanKindPlanFile     PlanKind = "plan_file"
)

// PlanBlock is a plan document preserved verbatim in the distilled output. The
// distiller re-attaches the full content as a trailing section so the summary
// LLM never has to paraphrase it. Plans come from the session's .plan.md files
// (served by the agent server), not from the transcript.
type PlanBlock struct {
	Index     int       // 1-based position
	Kind      PlanKind  // source kind (plan_file for session-dir documents)
	Timestamp time.Time // optional; zero for file plans
	Label     string    // short handle: the plan file's base name
	Content   string    // verbatim plan markdown
}

// planFileRegex matches paths that look like project plan documents.
// Both legs are anchored: a basename match or a docs/<name>-plan.md path.
var planFileRegex = regexp.MustCompile(`(?i)(^|/)(current_)?plan[^/]*\.md$|^docs/[^/]+-plan\.md$`)

// IsPlanFile reports whether the given file path matches the canonical
// plan-file pattern (CURRENT_PLAN.md, *plan*.md, docs/*-plan.md). The
// stamp-plan hook and StampPlanFile share this single regex source of truth.
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
			Kind:    PlanKindPlanFile,
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
		ts := ""
		if !block.Timestamp.IsZero() {
			ts = " @ " + block.Timestamp.UTC().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "### Plan #%d — %s%s\n\n", block.Index, block.Label, ts)
		b.WriteString(strings.TrimRight(block.Content, "\n"))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
