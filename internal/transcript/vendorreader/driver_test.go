package vendorreader

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestConvertJSONLLines_NilInfoEmitsNoSessionEvent(t *testing.T) {
	fr := &fakeRecorder{}
	err := ConvertJSONLLines(context.Background(), fr, [][]byte{[]byte("x")}, "codex", nil,
		func([]byte) error { return nil },
		func() error { return nil })
	require.NoError(t, err)
	for _, ev := range fr.recorded {
		assert.Nil(t, ev.Session, "a nil info must never produce a Session event")
	}
}

func TestConvertJSONLLines_EmitsSessionEventFirst(t *testing.T) {
	fr := &fakeRecorder{}
	info := &agent.ChatSessionInfo{SessionID: "s1"}
	var dispatched [][]byte
	err := ConvertJSONLLines(context.Background(), fr, [][]byte{[]byte("a"), []byte("b")}, "codex", info,
		func(line []byte) error { dispatched = append(dispatched, line); return nil },
		func() error { return nil })
	require.NoError(t, err)
	require.Len(t, fr.recorded, 1)
	require.NotNil(t, fr.recorded[0].Session)
	assert.Equal(t, "s1", fr.recorded[0].Session.SessionID)
	assert.Equal(t, [][]byte{[]byte("a"), []byte("b")}, dispatched)
}

func TestConvertJSONLLines_FlushRunsAfterAllLines(t *testing.T) {
	fr := &fakeRecorder{}
	var order []string
	err := ConvertJSONLLines(context.Background(), fr, [][]byte{[]byte("a")}, "codex", nil,
		func([]byte) error { order = append(order, "dispatch"); return nil },
		func() error { order = append(order, "flush"); return nil })
	require.NoError(t, err)
	assert.Equal(t, []string{"dispatch", "flush"}, order)
}

func TestConvertJSONLLines_DispatchErrorStopsAndSkipsFlush(t *testing.T) {
	fr := &fakeRecorder{}
	flushed := false
	err := ConvertJSONLLines(context.Background(), fr, [][]byte{[]byte("a")}, "codex", nil,
		func([]byte) error { return errors.New("bad line") },
		func() error { flushed = true; return nil })
	require.Error(t, err)
	assert.False(t, flushed, "flush must not run after a fatal dispatch error")
}

func TestConvertJSONLLines_ContextCancelledStopsBeforeDispatch(t *testing.T) {
	fr := &fakeRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := ConvertJSONLLines(ctx, fr, [][]byte{[]byte("a")}, "codex", nil,
		func([]byte) error { called = true; return nil },
		func() error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called)
}

// TestConvertJSONLLines_ContextCancelledMidStreamStopsAtThatLine pins the
// property a re-implementation of this loop is most likely to lose, and the
// only one no existing test covers: the cancellation check runs before EVERY
// line, not once before the first.
//
// This is the substance behind an earlier finding that adapters re-implemented
// this shared driver logic independently. Hoisting the ctx.Err() check out of the loop is a plausible
// tidy-up that leaves every other assertion in this file green while turning a
// cancelled import of a large transcript into one that runs to completion —
// and cancellation is one of only three conditions VendorAdapter's contract
// allows to abort a conversion at all.
func TestConvertJSONLLines_ContextCancelledMidStreamStopsAtThatLine(t *testing.T) {
	fr := &fakeRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	lines := [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}
	var dispatched [][]byte
	flushed := false

	err := ConvertJSONLLines(ctx, fr, lines, "codex", nil,
		func(line []byte) error {
			dispatched = append(dispatched, line)
			if len(dispatched) == 2 {
				cancel() // the caller gives up part-way through the file
			}
			return nil
		},
		func() error { flushed = true; return nil })

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, [][]byte{[]byte("a"), []byte("b")}, dispatched,
		"the check must run before every line, not only before the first")
	assert.False(t, flushed, "a cancelled conversion must not flush a partial boundary as if it were complete")
}
