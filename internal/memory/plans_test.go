package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

func TestIsPlanEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry backends.SessionEntry
		want  bool
	}{
		{
			name:  "user message is not a plan",
			entry: backends.SessionEntry{Type: backends.EntryTypeUser, Content: "## Plan\n1. step one\n2. step two"},
			want:  false,
		},
		{
			name: "ExitPlanMode tool use",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "ExitPlanMode",
				ToolInput: json.RawMessage(`{"plan":"step one"}`),
			},
			want: true,
		},
		{
			name: "TodoWrite tool use",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "TodoWrite",
				ToolInput: json.RawMessage(`{"todos":[]}`),
			},
			want: true,
		},
		{
			name: "TaskCreate tool use",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "TaskCreate",
				ToolInput: json.RawMessage(`{"description":"x"}`),
			},
			want: true,
		},
		{
			name: "TaskUpdate tool use",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "TaskUpdate",
				ToolInput: json.RawMessage(`{"id":"abc"}`),
			},
			want: true,
		},
		{
			name: "Write to CURRENT_PLAN.md",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"file_path":"/repo/CURRENT_PLAN.md","content":"plan"}`),
			},
			want: true,
		},
		{
			name: "Write to plan-anything.md",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"file_path":"/repo/plan-migration.md","content":"plan"}`),
			},
			want: true,
		},
		{
			name: "Write to docs/foo-plan.md",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"file_path":"docs/migration-plan.md","content":"plan"}`),
			},
			want: true,
		},
		{
			name: "Write to non-plan file",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"file_path":"/repo/README.md","content":"x"}`),
			},
			want: false,
		},
		{
			name: "Edit on plan file",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Edit",
				ToolInput: json.RawMessage(`{"file_path":"CURRENT_PLAN.md","old_string":"a","new_string":"b"}`),
			},
			want: true,
		},
		{
			name: "Edit on non-plan file",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Edit",
				ToolInput: json.RawMessage(`{"file_path":"main.go","old_string":"a","new_string":"b"}`),
			},
			want: false,
		},
		{
			name: "tool_result for plan tool is not a plan entry",
			entry: backends.SessionEntry{
				Type:       backends.EntryTypeToolResult,
				ToolName:   "ExitPlanMode",
				ToolOutput: "ok",
			},
			want: false,
		},
		{
			name: "Write with malformed input",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`not json`),
			},
			want: false,
		},
		{
			name: "Write with empty path",
			entry: backends.SessionEntry{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"content":"x"}`),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPlanEntry(tt.entry))
		})
	}
}

func TestExtractPlans_ExitPlanMode(t *testing.T) {
	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "hi"},
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "ExitPlanMode",
				ToolInput: json.RawMessage(`{"plan":"1. design schema\n2. migrate data"}`),
				Timestamp: time.Date(2026, 5, 25, 14, 30, 0, 0, time.UTC),
			},
		},
	}

	blocks := ExtractPlans(session)
	require.Len(t, blocks, 1)
	assert.Equal(t, 1, blocks[0].Index)
	assert.Equal(t, PlanKindExitPlanMode, blocks[0].Kind)
	assert.Equal(t, "ExitPlanMode", blocks[0].Label)
	assert.Equal(t, "1. design schema\n2. migrate data", blocks[0].Content)
}

func TestExtractPlans_TodoWritePrettyPrinted(t *testing.T) {
	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "TodoWrite",
				ToolInput: json.RawMessage(`{"todos":[{"id":"1","status":"pending","content":"do a thing"}]}`),
			},
		},
	}

	blocks := ExtractPlans(session)
	require.Len(t, blocks, 1)
	assert.Equal(t, PlanKindTodoWrite, blocks[0].Kind)
	// JSON pretty-printer indents 2 spaces.
	assert.Contains(t, blocks[0].Content, "\n  ")
	assert.Contains(t, blocks[0].Content, "\"do a thing\"")
}

func TestExtractPlans_WritePlanFile(t *testing.T) {
	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"file_path":"docs/sdk-migration-plan.md","content":"# Plan\n\nStep 1"}`),
			},
		},
	}

	blocks := ExtractPlans(session)
	require.Len(t, blocks, 1)
	assert.Equal(t, PlanKindPlanFile, blocks[0].Kind)
	assert.Equal(t, "sdk-migration-plan.md", blocks[0].Label)
	assert.Equal(t, "# Plan\n\nStep 1", blocks[0].Content)
}

func TestExtractPlans_EditPlanFile(t *testing.T) {
	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{
				Type:     backends.EntryTypeToolUse,
				ToolName: "Edit",
				ToolInput: json.RawMessage(`{
					"file_path":"CURRENT_PLAN.md",
					"old_string":"Step 1",
					"new_string":"Step 1 (done)"
				}`),
			},
		},
	}

	blocks := ExtractPlans(session)
	require.Len(t, blocks, 1)
	assert.Equal(t, PlanKindPlanFile, blocks[0].Kind)
	assert.Equal(t, "CURRENT_PLAN.md (edit)", blocks[0].Label)
	assert.Contains(t, blocks[0].Content, "Step 1 (done)")
	assert.Contains(t, blocks[0].Content, "Replaced:")
}

func TestExtractPlans_Ordering(t *testing.T) {
	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "go"},
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "TodoWrite",
				ToolInput: json.RawMessage(`{"todos":["a"]}`),
			},
			{Type: backends.EntryTypeAssistant, Content: "ok"},
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "ExitPlanMode",
				ToolInput: json.RawMessage(`{"plan":"final plan"}`),
			},
		},
	}

	blocks := ExtractPlans(session)
	require.Len(t, blocks, 2)
	assert.Equal(t, 1, blocks[0].Index)
	assert.Equal(t, PlanKindTodoWrite, blocks[0].Kind)
	assert.Equal(t, 2, blocks[1].Index)
	assert.Equal(t, PlanKindExitPlanMode, blocks[1].Kind)
}

func TestExtractPlans_SkipsEmptyContent(t *testing.T) {
	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "ExitPlanMode",
				ToolInput: json.RawMessage(`{"plan":""}`),
			},
			{
				Type:      backends.EntryTypeToolUse,
				ToolName:  "Write",
				ToolInput: json.RawMessage(`{"file_path":"plan.md","content":""}`),
			},
		},
	}

	blocks := ExtractPlans(session)
	assert.Empty(t, blocks, "blocks with empty content should be skipped")
}

func TestPlaceholderForBlock(t *testing.T) {
	block := PlanBlock{Index: 3, Label: "ExitPlanMode"}
	got := PlaceholderForBlock(block)
	assert.Equal(t, "[plan-block #3 — ExitPlanMode, preserved below]", got)
}

func TestRenderPlans_Empty(t *testing.T) {
	assert.Equal(t, "", RenderPlans(nil))
	assert.Equal(t, "", RenderPlans([]PlanBlock{}))
}

func TestRenderPlans_RoundTripPreservesContent(t *testing.T) {
	blocks := []PlanBlock{
		{
			Index:     1,
			Kind:      PlanKindExitPlanMode,
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
