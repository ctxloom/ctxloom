package coord

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBidiSession_RegisterAfterEndIsRefused pins the atomic step every
// session's teardown depends on: failPending answers the waiters it has and
// ends the session in ONE step, so a register that arrives after it is
// refused rather than parked forever.
func TestBidiSession_RegisterAfterEndIsRefused(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewBidiSession[string, string, string](cancel, 1)

	ch, ok := s.Register("r1", "req-1")
	require.True(t, ok)
	s.FailPending(func(id string) string { return "failed:" + id })
	assert.Equal(t, "failed:r1", <-ch, "an in-flight request is answered by the failure itself")

	_, ok = s.Register("r2", "req-2")
	assert.False(t, ok, "after the end, a waiter would hang forever: it is refused")
	assert.False(t, s.Resolve("r2", "late"), "nothing is registered to resolve")
}

// TestBidiSession_ResolveAndOutstanding: an answer reaches its waiter by id,
// a withdrawn waiter is forgotten, and outstanding names what a reconnect
// must reissue.
func TestBidiSession_ResolveAndOutstanding(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewBidiSession[string, string, string](cancel, 1)

	ch, ok := s.Register("r1", "req-1")
	require.True(t, ok)
	_, ok = s.Register("r2", "req-2")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"req-1", "req-2"}, s.Outstanding())

	s.Withdraw("r2")
	assert.Equal(t, []string{"req-1"}, s.Outstanding())
	require.True(t, s.Resolve("r1", "answer"))
	assert.Equal(t, "answer", <-ch)
	assert.Empty(t, s.Outstanding())
}

// TestBidiSession_PumpCancelsOnAWriteFailure: the single writer stops and
// cancels the session when the stream refuses a frame, and stops when the
// context ends.
func TestBidiSession_PumpCancelsOnAWriteFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewBidiSession[string, string, string](cancel, 4)
	s.send <- "first"
	s.send <- "second"
	var written []string
	s.Pump(ctx, func(f string) error {
		written = append(written, f)
		if f == "second" {
			return errors.New("stream closed")
		}
		return nil
	})
	assert.Equal(t, []string{"first", "second"}, written)
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "a failed write cancels the session")
}
