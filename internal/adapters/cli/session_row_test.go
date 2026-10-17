package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestSessionTime_TextVsJSON pins sessionTime's two-format posture: a compact
// second-granularity line for text/markdown (its Stringer), but a standard
// RFC3339 timestamp for json/yaml/toml (its MarshalJSON) — the fix that keeps
// `session list --format json` machine-parseable while `--format text` stays
// human-scannable.
func TestSessionTime_TextVsJSON(t *testing.T) {
	tm := sessionTime(time.Date(2026, 7, 17, 17, 27, 32, 0, time.UTC))

	assert.Equal(t, tm.String(), time.Time(tm).Local().Format("2006-01-02 15:04:05"))

	b, err := json.Marshal(tm)
	require.NoError(t, err)
	assert.Equal(t, `"2026-07-17T17:27:32Z"`, string(b))
}

// TestNewSessionRow_SummaryFallbackAndBadges pins the projection's business
// rules: an empty title becomes "(no summary)", and the purged and kept marks
// ride the summary as badges — on the row itself, so they show up in every
// --format.
func TestNewSessionRow_SummaryFallbackAndBadges(t *testing.T) {
	t.Run("no title falls back to placeholder", func(t *testing.T) {
		row := newSessionRow(operations.ViewSession(sessions.Entry{HarpName: "swift-amber-falcon"}))
		assert.Equal(t, "(no summary)", row.Summary)
		assert.Equal(t, "swift-amber-falcon", row.Harp)
	})

	t.Run("title carried through untouched", func(t *testing.T) {
		row := newSessionRow(viewTitled(sessions.Entry{HarpName: "h"}, "Fixed the bug"))
		assert.Equal(t, "Fixed the bug", row.Summary)
	})

	t.Run("a kept session carries the kept badge and flag", func(t *testing.T) {
		row := newSessionRow(viewTitled(sessions.Entry{HarpName: "h", Keep: true}, "Fixed the bug"))
		assert.Contains(t, row.Summary, "kept")
		assert.True(t, row.Kept)
	})
}

// TestNewSessionRow_EndedAt pins End's optional-pointer posture: unset for an
// in-progress session, populated for one that has ended.
func TestNewSessionRow_EndedAt(t *testing.T) {
	t.Run("no end for an in-progress session", func(t *testing.T) {
		row := newSessionRow(operations.ViewSession(sessions.Entry{HarpName: "h"}))
		assert.Nil(t, row.End)
	})

	t.Run("end populated once the session has ended", func(t *testing.T) {
		ended := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
		row := newSessionRow(operations.ViewSession(sessions.Entry{HarpName: "h", EndedAt: &ended}))
		require.NotNil(t, row.End)
		assert.True(t, time.Time(*row.End).Equal(ended))
	})
}

// TestSessionRow_JSONShape pins the wire shape of the projection:
// harp/summary/state/start, plus end, purged and kept once set (omitempty) —
// nothing from the fuller sessions.Entry (session_id, transcript_path, ...)
// leaks through, and nothing about a summary file: `summary` is the title.
func TestSessionRow_JSONShape(t *testing.T) {
	row := newSessionRow(viewTitled(sessions.Entry{
		HarpName:  "swift-amber-falcon",
		StartedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	}, "Designed the picker"))

	b, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.ElementsMatch(t, []string{"harp", "summary", "state", "start"}, keysOf(got),
		"end, purged and kept are omitted when unset, and no other Entry field leaks through (state is always present: the CLI row always asks the lock)")
	assert.Equal(t, "swift-amber-falcon", got["harp"])
	assert.Equal(t, "Designed the picker", got["summary"])
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
