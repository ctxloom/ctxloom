package agebound

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	t.Run("day offset", func(t *testing.T) {
		got, err := Parse("--older-than", "30d", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-30*24*time.Hour), got)
	})
	t.Run("week offset", func(t *testing.T) {
		got, err := Parse("--older-than", "2w", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-14*24*time.Hour), got)
	})
	t.Run("hour offset", func(t *testing.T) {
		got, err := Parse("--older-than", "720h", now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(-720*time.Hour), got)
	})
	t.Run("calendar date", func(t *testing.T) {
		got, err := Parse("--older-than", "2026-01-01", now)
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), got)
	})

	// Every rejection must produce a ZERO time WITH an error naming its
	// source. A parse that silently yielded zero-and-nil would arrive at the
	// operation as "no bound stated" — refused there, but for the wrong
	// reason and with the wrong message.
	for _, bad := range []string{"", "   ", "yesterday", "30x", "-5d", "0d", "01-01-2026"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			got, err := Parse("session_reap_age", bad, now)
			require.Error(t, err, "a bound that cannot be understood must fail loudly")
			assert.Contains(t, err.Error(), "session_reap_age")
			assert.True(t, got.IsZero())
		})
	}
}
