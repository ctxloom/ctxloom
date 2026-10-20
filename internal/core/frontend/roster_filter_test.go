package frontend

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

func TestParseListWindow(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute,
		" 7d ": 7 * 24 * time.Hour, "0": 0, "all": 0, "0d": 0,
	} {
		got, err := ParseListWindow(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "-1d", "soon", "7x", "2026-01-01", "ALL d"} {
		_, err := ParseListWindow(in)
		assert.ErrorIs(t, err, ErrInvalidListWindow, in)
	}
}

func TestNextListWindow(t *testing.T) {
	day := 24 * time.Hour
	for from, want := range map[time.Duration]time.Duration{
		0: time.Hour, time.Hour: day, day: 7 * day, 7 * day: 0,
		90 * time.Minute: day, 2 * 7 * day: 0, time.Minute: time.Hour,
	} {
		assert.Equal(t, want, NextListWindow(from), "from %v", from)
	}
}

func TestWindowLabel(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "any age", time.Hour: "1h", 24 * time.Hour: "1d", 14 * 24 * time.Hour: "14d",
		36 * time.Hour: "36h", 90 * time.Minute: "90m",
	} {
		assert.Equal(t, want, WindowLabel(d), "%v", d)
	}
}

func TestStatusFilter_Passes(t *testing.T) {
	rows := map[string]RosterRow{
		"live":      {State: SessionLive},
		"queued":    {State: coord.StateQueued},
		"executing": {State: coord.StateExecuting},
		"idle":      {State: coord.StateIdle},
		"ended":     {State: SessionEnded},
		"unknown":   {State: SessionUnknown},
		"asking":    {State: coord.StateIdle, Pending: 1},
	}
	for f, want := range map[StatusFilter][]string{
		StatusAll:     {"live", "queued", "executing", "idle", "ended", "unknown", "asking"},
		StatusActive:  {"live", "queued", "executing", "idle", "asking"},
		StatusWaiting: {"asking"},
		StatusEnded:   {"ended"},
	} {
		var got []string
		for name, r := range rows {
			if f.Passes(r) {
				got = append(got, name)
			}
		}
		assert.ElementsMatch(t, want, got, f.String())
	}
	assert.Equal(t, StatusAll, StatusEnded.Next(), "f wraps")
}
