package agentbus

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func listenTestBus(t *testing.T, hub *TapHub, roster RosterFunc, inject InjectFunc) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "bus.sock")
	srv, err := Listen(sock, hub, roster, inject)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestListen_ReplacesStaleSocket pins recovery from a dead predecessor's
// leftover socket file.
func TestListen_ReplacesStaleSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "bus.sock")
	srv1, err := Listen(sock, nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, srv1.Close())

	srv2, err := Listen(sock, nil, nil, nil)
	require.NoError(t, err)
	defer srv2.Close()

	got, err := FetchRoster(sock)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestSocket_SendRecvRetired pins the kill list: the shim's line-JSON
// send/recv verbs died with the agentcoord migration — the wire answers an
// explicit retirement error naming the replacement, never a silent accept.
func TestSocket_SendRecvRetired(t *testing.T) {
	sock := listenTestBus(t, nil, nil, nil)
	for _, op := range []string{"send", "recv"} {
		resp, err := roundTrip(sock, busRequest{Op: op, Harp: "child-a", Body: "hello"}, time.Second)
		require.NoError(t, err)
		assert.False(t, resp.OK)
		assert.Contains(t, resp.Error, "retired")
		assert.Contains(t, resp.Error, "CTXLOOM_COORD_URL")
	}
}

// TestSocket_ObserveRoundTrip drives the observe verb end to end over a real
// Unix socket: subscribe by harp, receive live entry/complete lines, and a
// final Ended when the child's stream closes — after which the events channel
// closes cleanly.
func TestSocket_ObserveRoundTrip(t *testing.T) {
	hub := NewTapHub()
	sock := listenTestBus(t, hub, nil, nil)

	in := make(chan agent.ChatEvent)
	out := hub.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()

	events, errs, err := ObserveSession(context.Background(), sock, "child-a")
	require.NoError(t, err)

	in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "live text"}}
	ev := requireObserveEvent(t, events)
	require.NotNil(t, ev.Entry)
	assert.Equal(t, agent.EntryTypeAssistant, ev.Entry.Type)
	assert.Equal(t, "live text", ev.Entry.Content)

	in <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	ev = requireObserveEvent(t, events)
	require.NotNil(t, ev.Complete)
	assert.Equal(t, "end_turn", ev.Complete.StopReason)

	close(in)
	ev = requireObserveEvent(t, events)
	assert.True(t, ev.Ended, "the stream must end with an explicit Ended line")

	select {
	case _, open := <-events:
		assert.False(t, open, "events must close after Ended")
	case <-time.After(2 * time.Second):
		t.Fatal("events channel never closed after Ended")
	}
	require.NoError(t, <-errs, "a clean end carries no transport error")
}

func requireObserveEvent(t *testing.T, events <-chan ObserveEvent) ObserveEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		require.True(t, ok, "observation stream closed early")
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no observation event arrived")
		return ObserveEvent{}
	}
}

// TestSocket_ObserveFanout: two concurrent observers of one harp each get the
// full stream.
func TestSocket_ObserveFanout(t *testing.T) {
	hub := NewTapHub()
	sock := listenTestBus(t, hub, nil, nil)

	in := make(chan agent.ChatEvent)
	out := hub.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()

	ev1, errs1, err := ObserveSession(context.Background(), sock, "child-a")
	require.NoError(t, err)
	ev2, errs2, err := ObserveSession(context.Background(), sock, "child-a")
	require.NoError(t, err)

	in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "for everyone"}}
	for _, events := range []<-chan ObserveEvent{ev1, ev2} {
		got := requireObserveEvent(t, events)
		require.NotNil(t, got.Entry)
		assert.Equal(t, "for everyone", got.Entry.Content)
	}
	close(in)
	assert.True(t, requireObserveEvent(t, ev1).Ended)
	assert.True(t, requireObserveEvent(t, ev2).Ended)
	require.NoError(t, <-errs1)
	require.NoError(t, <-errs2)
}

// TestSocket_ObserveNotLiveIsTyped pins the fallback contract: observing a
// harp the coordinator doesn't hold live (or a server with no hub at all)
// maps to ErrNotLive across the wire — the caller's cue to tail the store.
func TestSocket_ObserveNotLiveIsTyped(t *testing.T) {
	hub := NewTapHub()
	sock := listenTestBus(t, hub, nil, nil)
	_, _, err := ObserveSession(context.Background(), sock, "never-spawned")
	require.ErrorIs(t, err, ErrNotLive)

	bare := listenTestBus(t, nil, nil, nil) // no hub wired at all
	_, _, err = ObserveSession(context.Background(), bare, "anyone")
	require.ErrorIs(t, err, ErrNotLive)
}

// TestSocket_ObserveCancelDisconnects: cancelling the observer's context ends
// the stream client-side and releases the server-side subscription.
func TestSocket_ObserveCancelDisconnects(t *testing.T) {
	hub := NewTapHub()
	sock := listenTestBus(t, hub, nil, nil)

	in := make(chan agent.ChatEvent)
	out := hub.Tee("child-a", in)
	go func() {
		for range out {
		}
	}()
	defer close(in)

	ctx, cancel := context.WithCancel(context.Background())
	events, errs, err := ObserveSession(ctx, sock, "child-a")
	require.NoError(t, err)
	cancel()

	select {
	case _, open := <-events:
		assert.False(t, open, "cancel must close the stream")
	case <-time.After(2 * time.Second):
		t.Fatal("events channel never closed after cancel")
	}
	require.NoError(t, <-errs, "a deliberate cancel is not a transport error")

	// The server-side subscription is released: the tap's observer set drains.
	require.Eventually(t, func() bool {
		hub.mu.Lock()
		tp := hub.taps["child-a"]
		hub.mu.Unlock()
		require.NotNil(t, tp)
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return len(tp.observers) == 0
	}, 2*time.Second, 5*time.Millisecond)
}

// TestObserveLines_GapMarkerLeadsTheBatch pins the wire mapping: an overflow
// becomes one standalone gap line ahead of the surviving events, and
// non-transcript ChatEvents (Session/Permission) are skipped, not gap-counted.
func TestObserveLines_GapMarkerLeadsTheBatch(t *testing.T) {
	lines := observeLines(7, []agent.ChatEvent{
		{Session: &agent.ChatSessionInfo{Model: "m"}}, // lifecycle chatter: skipped
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "kept"}},
		{Complete: &agent.TurnMeta{StopReason: "end_turn"}},
	})
	require.Len(t, lines, 3)
	assert.Equal(t, 7, lines[0].Gap)
	require.NotNil(t, lines[1].Entry)
	assert.Equal(t, "kept", lines[1].Entry.Content)
	require.NotNil(t, lines[2].Complete)
}

// TestSocket_GapCrossesTheWire: a lagging observer's dropped-events count
// arrives as an explicit gap line ahead of the fresh events.
func TestSocket_GapCrossesTheWire(t *testing.T) {
	hub := NewTapHub()
	hub.ringSize = 4
	sock := listenTestBus(t, hub, nil, nil)

	in := make(chan agent.ChatEvent)
	out := hub.Tee("child-a", in)

	// A wire observer's ring only overflows behind a stalled socket write —
	// timing no hermetic test should lean on — so the overflow itself is
	// staged on a direct subscription and the wire mapping asserted on its
	// drained batch.
	ob, err := hub.Subscribe("child-a")
	require.NoError(t, err)
	defer ob.Close()

	// Draining out here (not in a goroutine) makes the publishes deterministic:
	// the pump publishes to observers before each forward.
	go func() {
		for i := 0; i < 10; i++ {
			in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: fmt.Sprintf("e%d", i)}}
		}
	}()
	for i := 0; i < 10; i++ {
		<-out
	}
	dropped, events, ok, terr := ob.Take(context.Background())
	require.NoError(t, terr)
	require.True(t, ok)
	assert.Equal(t, 6, dropped)

	lines := observeLines(dropped, events)
	require.Len(t, lines, 5)
	assert.Equal(t, 6, lines[0].Gap, "the gap marker leads")
	assert.Equal(t, "e6", lines[1].Entry.Content, "freshest events follow")

	// And the same shape survives a real wire trip: a live client sees its
	// events after the fact with no gap (its own ring never overflowed).
	events2, _, err := ObserveSession(context.Background(), sock, "child-a")
	require.NoError(t, err)
	go func() {
		in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "fresh"}}
		close(in)
	}()
	<-out
	got := requireObserveEvent(t, events2)
	assert.Zero(t, got.Gap)
	require.NotNil(t, got.Entry)
	assert.Equal(t, "fresh", got.Entry.Content)
}

// TestSocket_RosterRoundTrip pins the roster verb: the coordinator-supplied
// snapshot crosses the wire intact.
func TestSocket_RosterRoundTrip(t *testing.T) {
	want := []RosterEntry{
		{Harp: "swift-elm-fox", Agent: "developer", State: "executing", Parent: "coord", LastActivityUnix: 1751800000},
		{Harp: "deep-oak-hen", Agent: "finder", State: "parked", Parent: "coord", LastActivityUnix: 1751800100},
	}
	sock := listenTestBus(t, nil, func() []RosterEntry { return want }, nil)

	got, err := FetchRoster(sock)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestSocket_RosterWithoutProvider: a server with no roster source answers
// empty, not an error (nothing is held, nothing to list).
func TestSocket_RosterWithoutProvider(t *testing.T) {
	sock := listenTestBus(t, nil, nil, nil)
	got, err := FetchRoster(sock)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestSocket_InjectRoundTrip pins the inject verb's wire contract: the
// target harp and text reach the coordinator-side seam, the delivery mode
// it reports crosses back, and the typed not-injectable maps to its sentinel
// client-side.
func TestSocket_InjectRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var gotHarp, gotText string
	inject := func(harp, text string) (string, error) {
		if harp != "swift-elm-fox" {
			return "", fmt.Errorf("inject: %q is not a child of this coordinator: %w", harp, ErrNotInjectable)
		}
		mu.Lock()
		gotHarp, gotText = harp, text
		mu.Unlock()
		return DeliveryNewTurn, nil
	}
	sock := listenTestBus(t, nil, nil, inject)

	mode, err := Inject(sock, "swift-elm-fox", "user says: check the tokenizer")
	require.NoError(t, err)
	assert.Equal(t, DeliveryNewTurn, mode)
	mu.Lock()
	assert.Equal(t, "swift-elm-fox", gotHarp)
	assert.Equal(t, "user says: check the tokenizer", gotText)
	mu.Unlock()

	_, err = Inject(sock, "foreign-session-harp", "hello?")
	require.ErrorIs(t, err, ErrNotInjectable)
}

// TestSocket_InjectWithoutProvider: a server with no inject seam answers the
// typed not-injectable — nothing is held there, so nothing can be delivered.
func TestSocket_InjectWithoutProvider(t *testing.T) {
	sock := listenTestBus(t, nil, nil, nil)
	_, err := Inject(sock, "anyone", "hi")
	require.ErrorIs(t, err, ErrNotInjectable)
}
