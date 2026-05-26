package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// TestSelectClosestSession exercises the time-window match used by
// `ctxloom session distill <harp>` when no session_id was forward-bound
// during the session's lifetime. Pure-function tests; no backend
// registry involved.
func TestSelectClosestSession(t *testing.T) {
	t0 := time.Date(2026, 5, 26, 14, 32, 0, 0, time.UTC)
	harpEnded := t0.Add(30 * time.Minute)

	metas := []backends.SessionMeta{
		// Far outside the window — should never match.
		{ID: "ancient", StartTime: t0.Add(-24 * time.Hour)},
		// Slightly off but within the window.
		{ID: "close-start-only", StartTime: t0.Add(10 * time.Second)},
		// Perfect start match, slightly off end.
		{ID: "perfect-start", StartTime: t0, EndTime: harpEnded.Add(45 * time.Second)},
		// Better composite score: small start drift, end matches.
		{ID: "best-composite", StartTime: t0.Add(2 * time.Second), EndTime: harpEnded},
	}

	t.Run("uses_end_to_break_ties", func(t *testing.T) {
		got := selectClosestSession(metas, t0, &harpEnded, 5*time.Minute)
		// perfect-start has startDiff=0 + endDiff=45s = 45s.
		// best-composite has startDiff=2s + endDiff=0 = 2s.
		// Best wins.
		assert.Equal(t, "best-composite", got)
	})

	t.Run("ignores_end_when_nil", func(t *testing.T) {
		got := selectClosestSession(metas, t0, nil, 5*time.Minute)
		// Without end tie-break, perfect-start (startDiff=0) wins.
		assert.Equal(t, "perfect-start", got)
	})

	t.Run("returns_empty_when_no_match_in_window", func(t *testing.T) {
		got := selectClosestSession(metas, t0.Add(-12*time.Hour), nil, 5*time.Minute)
		assert.Empty(t, got, "no SessionMeta within window of -12h should yield no match")
	})

	t.Run("ignores_metas_outside_window", func(t *testing.T) {
		// Only one in-window candidate.
		near := []backends.SessionMeta{
			{ID: "way-off", StartTime: t0.Add(-10 * time.Minute)},
			{ID: "near", StartTime: t0.Add(30 * time.Second)},
		}
		got := selectClosestSession(near, t0, nil, 5*time.Minute)
		assert.Equal(t, "near", got)
	})

	t.Run("handles_empty_input", func(t *testing.T) {
		assert.Empty(t, selectClosestSession(nil, t0, nil, 5*time.Minute))
	})

	t.Run("window_zero_is_unmatchable", func(t *testing.T) {
		// startDiff > 0 for any non-exact match → ruled out by `> window`.
		got := selectClosestSession([]backends.SessionMeta{
			{ID: "off-by-one-ns", StartTime: t0.Add(1)},
		}, t0, nil, 0)
		assert.Empty(t, got)
	})
}

func TestAbsDuration(t *testing.T) {
	assert.Equal(t, 5*time.Second, absDuration(5*time.Second))
	assert.Equal(t, 5*time.Second, absDuration(-5*time.Second))
	assert.Equal(t, time.Duration(0), absDuration(0))
}
