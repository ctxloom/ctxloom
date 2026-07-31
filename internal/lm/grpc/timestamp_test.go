package grpc

import (
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Entry, session and metadata timestamps cross this wire as UNIX SECONDS, with
// 0 doubling as the "no timestamp" sentinel. That representation is lossy in
// two ways this test makes explicit rather than leaving to be rediscovered.
// Both are properties of the wire field itself (proto int64 seconds), so
// neither can be corrected without a schema decision; until one is taken, no
// consumer may assume a round-tripped timestamp is the one that was sent.
func TestTimestampWire_LossIsBounded(t *testing.T) {
	t.Run("the unix epoch is indistinguishable from no timestamp", func(t *testing.T) {
		epoch := time.Unix(0, 0).UTC()
		require.False(t, epoch.IsZero(), "the epoch is a real instant, not Go's zero time")

		assert.Equal(t, int64(0), timeToUnix(epoch))
		assert.True(t, unixToTime(timeToUnix(epoch)).IsZero(),
			"a genuine 1970-01-01T00:00:00Z round-trips to the zero time; 0 is overloaded as the sentinel")
	})

	t.Run("sub-second precision is dropped", func(t *testing.T) {
		a := time.Date(2026, 7, 30, 12, 0, 0, 100_000_000, time.UTC)
		b := time.Date(2026, 7, 30, 12, 0, 0, 900_000_000, time.UTC)
		require.True(t, a.Before(b))

		assert.Equal(t, unixToTime(timeToUnix(a)), unixToTime(timeToUnix(b)),
			"two entries written in the same second arrive at the same instant")
	})

	t.Run("whole seconds round-trip exactly", func(t *testing.T) {
		want := time.Date(2026, 7, 30, 12, 34, 56, 0, time.UTC)
		got := entryFromProto(EntryToProto(agent.SessionEntry{Timestamp: want})).Timestamp
		assert.True(t, want.Equal(got), "whole seconds are the part that IS preserved")
	})
}
