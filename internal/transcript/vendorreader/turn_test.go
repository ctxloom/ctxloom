package vendorreader

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestFlushComplete_NilPendingIsNoOp(t *testing.T) {
	var pending *agent.TurnMeta
	var called bool
	record := func(agent.ChatEvent) error { called = true; return nil }

	require.NoError(t, FlushComplete(&pending, record))
	assert.False(t, called, "a nil pending must never invoke record")
}

func TestFlushComplete_RecordsAndClears(t *testing.T) {
	pending := &agent.TurnMeta{InputTokens: 42}
	var got agent.ChatEvent
	record := func(ev agent.ChatEvent) error { got = ev; return nil }

	require.NoError(t, FlushComplete(&pending, record))
	assert.Nil(t, pending, "pending must be cleared after a successful flush")
	require.NotNil(t, got.Complete)
	assert.Equal(t, 42, got.Complete.InputTokens)
}

func TestFlushComplete_PropagatesRecordError(t *testing.T) {
	pending := &agent.TurnMeta{}
	record := func(agent.ChatEvent) error { return errors.New("boom") }

	err := FlushComplete(&pending, record)
	require.Error(t, err)
	assert.Equal(t, "boom", err.Error())
}

// TestFlushComplete_FailedFlushKeepsThePendingBoundary pins the half of the
// contract that was inverted. FlushComplete cleared *pending BEFORE record
// returned, so a failed flush destroyed the boundary it failed to write: the
// caller was left with no error-free way to retry, and a retry that did happen
// found nil pending and returned nil — a success for zero bytes written, this
// project's signature failure shape, in the one helper whose stated purpose is
// that "a still-open boundary at end of file is real, captured data, not
// something to silently drop".
func TestFlushComplete_FailedFlushKeepsThePendingBoundary(t *testing.T) {
	pending := &agent.TurnMeta{InputTokens: 7}
	fail := errors.New("sink down")
	var attempts int
	record := func(agent.ChatEvent) error {
		attempts++
		if attempts == 1 {
			return fail
		}
		return nil
	}

	require.ErrorIs(t, FlushComplete(&pending, record), fail)
	require.NotNil(t, pending, "a failed flush must not destroy the boundary it could not write")
	assert.Equal(t, 7, pending.InputTokens, "and must not corrupt it either")

	// The retry is the whole point: it must actually re-attempt the write.
	require.NoError(t, FlushComplete(&pending, record))
	assert.Equal(t, 2, attempts, "the second flush must reach record, not no-op on a cleared pending")
	assert.Nil(t, pending, "and clears only once the write succeeded")
}
