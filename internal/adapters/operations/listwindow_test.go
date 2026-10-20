package operations

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
