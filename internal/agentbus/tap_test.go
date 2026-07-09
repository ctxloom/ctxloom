package agentbus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func entryEv(content string) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: content}}
}

func completeEv() agent.ChatEvent {
	return agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
}

// takeAll drains one Take with a deadline, failing the test on timeout.
func takeAll(t *testing.T, ob *TapObserver) (int, []agent.ChatEvent, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dropped, events, ok, err := ob.Take(ctx)
	require.NoError(t, err, "Take must not time out")
	return dropped, events, ok
}

// TestTee_ForwardsAllEventsInOrder pins the primary path: the orchestrator's
// consumption sees every event, in order, whether or not anyone observes.
func TestTee_ForwardsAllEventsInOrder(t *testing.T) {
	h := NewTapHub()
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)

	go func() {
		for i := 0; i < 10; i++ {
			in <- entryEv(fmt.Sprintf("e%d", i))
		}
		close(in)
	}()

	var got []string
	for ev := range out {
		got = append(got, ev.Entry.Content)
	}
	require.Len(t, got, 10)
	for i, c := range got {
		assert.Equal(t, fmt.Sprintf("e%d", i), c)
	}
}

// TestTee_StuckObserverNeverBlocksDelivery pins the seam's non-negotiable
// invariant: an observer that never reads must not delay the orchestrator's
// own consumption — and when it finally reads, it gets the freshest events
// plus an explicit gap count for everything it missed.
func TestTee_StuckObserverNeverBlocksDelivery(t *testing.T) {
	h := NewTapHub()
	h.ringSize = 8 // small ring so the stall overflows quickly
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)

	ob, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob.Close()
	// ob is now stuck: it never Takes while delivery runs.

	const total = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < total; i++ {
			in <- entryEv(fmt.Sprintf("e%d", i))
		}
	}()

	// The orchestrator side must receive every event promptly despite the
	// stuck observer.
	for i := 0; i < total; i++ {
		select {
		case ev := <-out:
			assert.Equal(t, fmt.Sprintf("e%d", i), ev.Entry.Content)
		case <-time.After(2 * time.Second):
			t.Fatalf("delivery stalled at event %d behind a stuck observer", i)
		}
	}
	<-done

	// The stuck observer's ring kept the NEWEST ringSize events and reports
	// the rest as an explicit gap.
	dropped, events, ok := takeAll(t, ob)
	require.True(t, ok)
	assert.Equal(t, total-h.ringSize, dropped, "everything evicted is counted, never silent")
	require.Len(t, events, h.ringSize)
	assert.Equal(t, fmt.Sprintf("e%d", total-h.ringSize), events[0].Entry.Content, "drop-oldest keeps the freshest tail")
	assert.Equal(t, fmt.Sprintf("e%d", total-1), events[len(events)-1].Entry.Content)
}

// TestTee_MultiObserverFanout pins independent fanout: concurrent observers
// each see the full stream.
func TestTee_MultiObserverFanout(t *testing.T) {
	h := NewTapHub()
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()

	ob1, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob1.Close()
	ob2, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob2.Close()

	const total = 20
	go func() {
		for i := 0; i < total; i++ {
			in <- entryEv(fmt.Sprintf("e%d", i))
		}
		close(in)
	}()

	for _, ob := range []*TapObserver{ob1, ob2} {
		var got []string
		for len(got) < total {
			dropped, events, ok := takeAll(t, ob)
			require.True(t, ok, "stream must not end before all events arrive")
			require.Zero(t, dropped, "nothing overflows at this volume")
			for _, ev := range events {
				got = append(got, ev.Entry.Content)
			}
		}
		require.Len(t, got, total)
		for i, c := range got {
			assert.Equal(t, fmt.Sprintf("e%d", i), c)
		}
	}
}

// TestTee_SubscribeDeliversForwardOnly pins the seam contract: a subscription
// starts at subscribe-time — earlier events are the store's job.
func TestTee_SubscribeDeliversForwardOnly(t *testing.T) {
	h := NewTapHub()
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)

	in <- entryEv("before")
	<-out

	ob, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob.Close()

	in <- entryEv("after")
	<-out

	dropped, events, ok := takeAll(t, ob)
	require.True(t, ok)
	assert.Zero(t, dropped)
	require.Len(t, events, 1)
	assert.Equal(t, "after", events[0].Entry.Content)
}

// TestTee_EndMarksObserversAndUnregisters: when the child's stream closes the
// observers drain what is buffered, then see the end; new subscriptions get
// the typed not-live error.
func TestTee_EndMarksObserversAndUnregisters(t *testing.T) {
	h := NewTapHub()
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()

	ob, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob.Close()

	in <- entryEv("last words")
	close(in)

	// Buffered events still drain after the end...
	var got []agent.ChatEvent
	for {
		_, events, ok := takeAll(t, ob)
		if !ok {
			break
		}
		got = append(got, events...)
	}
	require.Len(t, got, 1)
	assert.Equal(t, "last words", got[0].Entry.Content)

	// ...and the harp is no longer live.
	require.Eventually(t, func() bool {
		_, serr := h.Subscribe("child-a")
		return serr != nil
	}, 2*time.Second, 5*time.Millisecond)
	_, err = h.Subscribe("child-a")
	require.ErrorIs(t, err, ErrNotLive)
}

// TestSubscribe_UnknownHarpIsNotLive pins the typed error for a harp never
// teed here.
func TestSubscribe_UnknownHarpIsNotLive(t *testing.T) {
	h := NewTapHub()
	_, err := h.Subscribe("nobody-home")
	require.ErrorIs(t, err, ErrNotLive)
}

// TestTee_ResumeReplacesEndedTap pins the resume path: a re-Tee of the same
// harp registers the fresh stream, observable again.
func TestTee_ResumeReplacesEndedTap(t *testing.T) {
	h := NewTapHub()

	in1 := make(chan agent.ChatEvent)
	out1 := h.Tee("child-a", in1)
	close(in1)
	for range out1 {
	}

	in2 := make(chan agent.ChatEvent)
	out2 := h.Tee("child-a", in2)
	go func() {
		for range out2 {
		}
	}()
	defer close(in2)

	ob, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob.Close()

	in2 <- entryEv("resumed")
	_, events, ok := takeAll(t, ob)
	require.True(t, ok)
	require.Len(t, events, 1)
	assert.Equal(t, "resumed", events[0].Entry.Content)
}

// TestTake_CtxCancelUnblocks pins that an idle Take honors its context (the
// socket handler's disconnect path).
func TestTake_CtxCancelUnblocks(t *testing.T) {
	h := NewTapHub()
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()
	defer close(in)

	ob, err := h.Subscribe("child-a")
	require.NoError(t, err)
	defer ob.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, _, _, terr := ob.Take(ctx)
	require.ErrorIs(t, terr, context.Canceled)
}

// TestTee_ClosedObserverStopsBuffering: a Closed observer no longer
// accumulates (no leak from an abandoned subscription).
func TestTee_ClosedObserverStopsBuffering(t *testing.T) {
	h := NewTapHub()
	in := make(chan agent.ChatEvent)
	out := h.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()
	defer close(in)

	ob, err := h.Subscribe("child-a")
	require.NoError(t, err)
	ob.Close()

	in <- entryEv("unseen")
	in <- completeEv()

	ob.mu.Lock()
	n := ob.n
	ob.mu.Unlock()
	assert.Zero(t, n, "a detached observer buffers nothing")
}
