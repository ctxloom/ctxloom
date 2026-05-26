package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// PlanKind identifies what surface produced a preserved plan block.
type PlanKind string

const (
	PlanKindExitPlanMode PlanKind = "exit_plan_mode"
	PlanKindTodoWrite    PlanKind = "todo_write"
	PlanKindPlanFile     PlanKind = "plan_file"
)

// PlanBlock is a chunk of plan content lifted verbatim from a session
// transcript. The distiller emits a placeholder where the block appeared
// and re-attaches the full content as a trailing section so the summary
// LLM never has to paraphrase it.
type PlanBlock struct {
	Index     int       // 1-based position within the session
	Kind      PlanKind  // Detection source
	Timestamp time.Time // Entry timestamp (zero if not parseable)
	Label     string    // Short handle: tool name, file basename, etc.
	Content   string    // Verbatim plan text

	// entryIdx is the index into session.Entries where this block lives,
	// used to drive placeholder insertion. Unexported because callers
	// outside this package have no business with it.
	entryIdx int
}

// planFileRegex matches paths that look like project plan documents.
// Both legs are anchored: a basename match or a docs/<name>-plan.md path.
var planFileRegex = regexp.MustCompile(`(?i)(^|/)(current_)?plan[^/]*\.md$|^docs/[^/]+-plan\.md$`)

// IsPlanFile reports whether the given file path matches the canonical
// plan-file pattern (CURRENT_PLAN.md, *plan*.md, docs/*-plan.md). Used by
// callers outside this package (e.g. the stamp-plan hook in internal/tasks)
// to share the single regex source of truth.
func IsPlanFile(path string) bool {
	return planFileRegex.MatchString(path)
}

// IsPlanEntry reports whether an entry should be preserved verbatim.
func IsPlanEntry(entry backends.SessionEntry) bool {
	if entry.Type != backends.EntryTypeToolUse {
		return false
	}
	switch entry.ToolName {
	case "ExitPlanMode", "TodoWrite", "TaskCreate", "TaskUpdate":
		return true
	case "Write", "Edit":
		return inputMatchesPlanFile(entry.ToolInput)
	}
	return false
}

func inputMatchesPlanFile(input json.RawMessage) bool {
	if len(input) == 0 {
		return false
	}
	var parsed struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &parsed); err != nil || parsed.FilePath == "" {
		return false
	}
	return planFileRegex.MatchString(parsed.FilePath)
}

// ExtractPlans returns each plan block found in the session, in order.
func ExtractPlans(session *backends.Session) []PlanBlock {
	if session == nil {
		return nil
	}
	var blocks []PlanBlock
	for i, entry := range session.Entries {
		if !IsPlanEntry(entry) {
			continue
		}
		block, ok := planFromEntry(entry, len(blocks)+1)
		if !ok {
			continue
		}
		block.entryIdx = i
		blocks = append(blocks, block)
	}
	return blocks
}

func planFromEntry(entry backends.SessionEntry, index int) (PlanBlock, bool) {
	block := PlanBlock{Index: index, Timestamp: entry.Timestamp}

	switch entry.ToolName {
	case "ExitPlanMode":
		var parsed struct {
			Plan string `json:"plan"`
		}
		if err := json.Unmarshal(entry.ToolInput, &parsed); err != nil || parsed.Plan == "" {
			return block, false
		}
		block.Kind = PlanKindExitPlanMode
		block.Label = "ExitPlanMode"
		block.Content = parsed.Plan
		return block, true

	case "TodoWrite", "TaskCreate", "TaskUpdate":
		pretty, err := prettyJSON(entry.ToolInput)
		if err != nil || pretty == "" {
			return block, false
		}
		block.Kind = PlanKindTodoWrite
		block.Label = entry.ToolName
		block.Content = pretty
		return block, true

	case "Write":
		var parsed struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(entry.ToolInput, &parsed); err != nil || parsed.Content == "" {
			return block, false
		}
		block.Kind = PlanKindPlanFile
		block.Label = filepath.Base(parsed.FilePath)
		block.Content = parsed.Content
		return block, true

	case "Edit":
		var parsed struct {
			FilePath  string `json:"file_path"`
			NewString string `json:"new_string"`
			OldString string `json:"old_string"`
		}
		if err := json.Unmarshal(entry.ToolInput, &parsed); err != nil || parsed.NewString == "" {
			return block, false
		}
		block.Kind = PlanKindPlanFile
		block.Label = filepath.Base(parsed.FilePath) + " (edit)"
		// We don't have the full post-edit file from a JSONL entry, so record
		// the patch verbatim. Better than letting the summary paraphrase it.
		var b strings.Builder
		fmt.Fprintf(&b, "**Edit applied to %s**\n\n", parsed.FilePath)
		b.WriteString("Replaced:\n```\n")
		b.WriteString(parsed.OldString)
		b.WriteString("\n```\n\nWith:\n```\n")
		b.WriteString(parsed.NewString)
		b.WriteString("\n```")
		block.Content = b.String()
		return block, true
	}

	return block, false
}

func prettyJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// PlaceholderForBlock returns the marker that takes the block's place in the
// chunked text sent to the LLM. The distiller prompt instructs the model to
// reference these by number rather than paraphrase them.
func PlaceholderForBlock(block PlanBlock) string {
	return fmt.Sprintf("[plan-block #%d — %s, preserved below]", block.Index, block.Label)
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

// indexBlocksByEntry returns a map from session-entry index to the
// corresponding plan block, for placeholder insertion in sessionToText.
func indexBlocksByEntry(blocks []PlanBlock) map[int]PlanBlock {
	m := make(map[int]PlanBlock, len(blocks))
	for _, b := range blocks {
		m[b.entryIdx] = b
	}
	return m
}
