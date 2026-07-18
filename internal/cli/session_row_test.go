package cli

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
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

// TestNewSessionRow_SummaryFallbackAndStaleBadge pins the projection's
// business rules carried over from the old renderSessionTable: an empty
// summary becomes "(no summary)", and a stale essence (SourceStale) gets the
// "out of date" badge appended — both now live on the row itself, not just
// the text renderer, so they show up in every --format.
func TestNewSessionRow_SummaryFallbackAndStaleBadge(t *testing.T) {
	t.Run("no summary falls back to placeholder", func(t *testing.T) {
		row := newSessionRow(sessions.Entry{HarpName: "swift-amber-falcon"})
		assert.Equal(t, "(no summary)", row.Summary)
		assert.Equal(t, "swift-amber-falcon", row.Harp)
	})

	t.Run("summary carried through untouched when fresh", func(t *testing.T) {
		row := newSessionRow(sessions.Entry{HarpName: "h", Summary: "Fixed the bug"})
		assert.Equal(t, "Fixed the bug", row.Summary)
	})

	t.Run("stale essence gets the out-of-date badge", func(t *testing.T) {
		dir := t.TempDir()
		transcript := dir + "/transcript.jsonl"
		require.NoError(t, os.WriteFile(transcript, []byte("grown past the stamped size"), 0o644))
		row := newSessionRow(sessions.Entry{
			HarpName:       "h",
			Summary:        "Fixed the bug",
			TranscriptPath: transcript,
			SourceSize:     1, // essence was stamped when the transcript was 1 byte; it has grown since
		})
		assert.Contains(t, row.Summary, "out of date")
	})
}

// TestNewSessionRow_EndedAt pins End's optional-pointer posture: unset for an
// in-progress session, populated for one that has ended.
func TestNewSessionRow_EndedAt(t *testing.T) {
	t.Run("no end for an in-progress session", func(t *testing.T) {
		row := newSessionRow(sessions.Entry{HarpName: "h"})
		assert.Nil(t, row.End)
	})

	t.Run("end populated once the session has ended", func(t *testing.T) {
		ended := time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)
		row := newSessionRow(sessions.Entry{HarpName: "h", EndedAt: &ended})
		require.NotNil(t, row.End)
		assert.True(t, time.Time(*row.End).Equal(ended))
	})
}

// TestSessionRow_JSONShape pins the wire shape of the new default projection:
// exactly harp/summary/start/end, nothing from the fuller sessions.Entry
// (session_id, transcript_path, distilled, etc.) leaks through.
func TestSessionRow_JSONShape(t *testing.T) {
	row := newSessionRow(sessions.Entry{
		HarpName:  "swift-amber-falcon",
		Summary:   "Designed the picker",
		StartedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})

	b, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.ElementsMatch(t, []string{"harp", "summary", "start"}, keysOf(got),
		"end must be omitted (omitempty) when the session hasn't ended, and no other Entry field should leak through")
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
