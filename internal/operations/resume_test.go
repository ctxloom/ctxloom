package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestRenderResumedTranscript_EmptyEntriesWarns is a regression guard: a
// transcript that yields zero substantive (user/assistant/
// tool-use) entries used to render "" with NO signal to the operator —
// RenderResumedTranscript now warns via clidiag when it degrades to nothing,
// so every one of its three consumers (operations/engine_session.go,
// agentcoord/coord/spawner.go, cli/run.go) gets the signal automatically
// without each having to add its own rendered=="" check.
func TestRenderResumedTranscript_EmptyEntriesWarns(t *testing.T) {
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	// Entries exist, but none are substantive (only a thinking entry, which
	// RenderResumedTranscript deliberately excludes) — MainThreadEntries'
	// real degrade case (an all-sidechain or otherwise empty transcript)
	// looks the same from RenderResumedTranscript's point of view: zero parts.
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeThinking, Content: "pondering"},
	}

	rendered := RenderResumedTranscript("test-harp", entries)
	assert.Empty(t, rendered, "no substantive entries must still render empty")
	assert.Contains(t, buf.String(), "test-harp",
		"an empty render must not be silent — every consumer relies on this warning instead of its own check")
}

// TestRenderResumedTranscript_LastEntryOverBudgetStillIncluded is a
// regression guard: the tail-budget loop initialized start to
// len(parts) and only advanced it AFTER the budget test passed, so a single
// trailing entry larger than resumeTranscriptBudget broke on its first
// iteration before start ever moved — parts[start:] was empty, yet the
// header ("its recorded history follows") and truncation note still
// printed. The fix must always include at least the last entry.
func TestRenderResumedTranscript_LastEntryOverBudgetStillIncluded(t *testing.T) {
	huge := strings.Repeat("x", resumeTranscriptBudget+1024)
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: huge},
	}

	rendered := RenderResumedTranscript("test-harp", entries)
	require.NotEmpty(t, rendered)
	assert.Contains(t, rendered, "its recorded history follows")
	// The header must not be a lie: SOME of the oversized entry's own text
	// must actually appear in the body, not just the announcement.
	assert.True(t, strings.Contains(rendered, "x"),
		"a single trailing entry over budget must still contribute content, not just an empty announcement")
}

// TestRenderResumedTranscript_TailBudgetKeepsMostRecent is a non-regression
// check that the ordinary (multi-entry, under-budget) tail-selection
// behavior is unchanged: earlier entries are dropped, the most recent ones
// (and only those) are kept, in original order.
func TestRenderResumedTranscript_TailBudgetKeepsMostRecent(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "first"},
		{Type: agent.EntryTypeAssistant, Content: "second"},
		{Type: agent.EntryTypeUser, Content: "third"},
	}

	rendered := RenderResumedTranscript("test-harp", entries)
	require.NotEmpty(t, rendered)
	assert.Contains(t, rendered, "user: first")
	assert.Contains(t, rendered, "assistant: second")
	assert.Contains(t, rendered, "user: third")
	assert.NotContains(t, rendered, "truncated", "everything fits comfortably under the 32KiB budget")
}
