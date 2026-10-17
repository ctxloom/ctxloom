package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// readSessionPlans reads a harp's own *.plan.md documents straight from its
// ctxloom session directory (shared/plans.SessionPlanPaths). This is a pure
// filesystem read — plans are the session's authored artifacts, never mined
// from the transcript and never fetched over a plugin — so the compactor no
// longer needs an engine reader to attach them. An empty harp and a genuinely
// absent session directory are quietly "no plans"; a file present but
// unreadable is a per-file warning, not a failed compaction.
//
// It reads the OS filesystem, not the compactor's fs: its candidates come
// from plans.SessionPlanPaths, which applies symlink containment against the
// real filesystem, and reading them through a different fs would let listing
// and reading disagree.
func readSessionPlans(harp string) []agent.PlanFile {
	if harp == "" {
		return nil
	}
	candidates, problems := plans.SessionPlanPaths(harp)
	var out []agent.PlanFile
	for _, path := range candidates {
		base := filepath.Base(path)
		content, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("plan file %s omitted, unreadable: %w", base, err))
			continue
		}
		out = append(out, agent.PlanFile{
			Name:    strings.TrimSuffix(base, paths.PlanFileExt),
			Content: string(content),
		})
	}
	for _, err := range problems {
		clidiag.Warn("ctxloom", "%v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PlanBlock is a plan document preserved verbatim in the compacted output. The
// distiller re-attaches the full content as a trailing section so the summary
// LLM never has to paraphrase it. Plans come from the session's .plan.md files
// (served by the agent server), not from the transcript.
type PlanBlock struct {
	Index   int    // 1-based position
	Label   string // short handle: the plan file's base name
	Content string // verbatim plan markdown
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
// compacted output. Returns the empty string when no blocks were found.
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
