package transcript

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionWith builds a session whose entries are user/assistant turns with the
// given contents, enough to exercise the diff/boundary logic.
func sessionWith(contents ...string) *agent.Session {
	s := &agent.Session{ID: "s1"}
	for _, c := range contents {
		s.Entries = append(s.Entries, agent.SessionEntry{Type: agent.SessionEntryType("assistant"), Content: c})
	}
	return s
}

// --- pure sessionWatcher.step logic ---

// TestSessionWatcher_StreamsOnlyNewEntries: each poll emits just the entries that
// appeared since the last one, never re-emitting what was already streamed.
func TestSessionWatcher_StreamsOnlyNewEntries(t *testing.T) {
	w := &sessionWatcher{heartbeatEvery: watchHeartbeatEvery}

	first := w.step(sessionWith("a", "b"))
	require.Len(t, first, 2)
	assert.Equal(t, "a", first[0].Entry.Content)
	assert.Equal(t, "b", first[1].Entry.Content)

	// Transcript grew by one: only the new entry streams.
	second := w.step(sessionWith("a", "b", "c"))
	require.Len(t, second, 1)
	assert.Equal(t, "c", second[0].Entry.Content)
}

// TestSessionWatcher_BoundaryFiresWhenGrowthStalls: once the transcript stops
// growing and material has accumulated since the last boundary, a single
// ResponseBoundary marks the just-completed response [from,to); subsequent idle
// polls do not re-emit it.
func TestSessionWatcher_BoundaryFiresWhenGrowthStalls(t *testing.T) {
	w := &sessionWatcher{heartbeatEvery: watchHeartbeatEvery}

	w.step(sessionWith("a", "b")) // stream 2 entries
	sess := sessionWith("a", "b")

	// Growth stalled: boundary over [0,2).
	stall := w.step(sess)
	require.Len(t, stall, 1)
	b := stall[0].Boundary
	require.NotNil(t, b, "first stalled poll must emit a boundary")
	assert.Equal(t, 0, b.FromIndex)
	assert.Equal(t, 2, b.ToIndex)

	// Still idle: no second boundary for the same material.
	again := w.step(sess)
	for _, ev := range again {
		assert.Nil(t, ev.Boundary, "a settled response must not re-emit a boundary")
	}
}

// TestSessionWatcher_BoundaryPerResponse: a second burst after a boundary gets
// its own boundary spanning only the new entries.
func TestSessionWatcher_BoundaryPerResponse(t *testing.T) {
	w := &sessionWatcher{heartbeatEvery: watchHeartbeatEvery}

	w.step(sessionWith("a"))               // stream entry 0
	w.step(sessionWith("a"))               // boundary [0,1)
	w.step(sessionWith("a", "b"))          // stream entry 1
	stall := w.step(sessionWith("a", "b")) // boundary [1,2)

	require.Len(t, stall, 1)
	b := stall[0].Boundary
	require.NotNil(t, b)
	assert.Equal(t, 1, b.FromIndex)
	assert.Equal(t, 2, b.ToIndex)
}

// TestSessionWatcher_HeartbeatOnSlowCadence: a fully-idle session (nothing
// pending a boundary) emits heartbeats only once every heartbeatEvery polls.
func TestSessionWatcher_HeartbeatOnSlowCadence(t *testing.T) {
	w := &sessionWatcher{heartbeatEvery: 3}
	empty := sessionWith()

	var beats int
	for i := 0; i < 6; i++ {
		for _, ev := range w.step(empty) {
			if ev.Heartbeat {
				beats++
			}
		}
	}
	assert.Equal(t, 2, beats, "6 idle polls at cadence 3 → 2 heartbeats")
}

// TestSessionWatcher_NilSessionIsIdle: a nil session (e.g. not yet materialized)
// is treated as empty/idle, not a crash.
func TestSessionWatcher_NilSessionIsIdle(t *testing.T) {
	w := &sessionWatcher{heartbeatEvery: 0}
	events := w.step(nil)
	require.Len(t, events, 1)
	assert.True(t, events[0].Heartbeat)
}

// --- WatchSession streaming handler ---

// fakeHistory is an agent.SessionHistory whose reads are scripted: by id
// (getSessionFunc, what EngineReader.WatchSession polls) and by path
// (getSessionByPathFunc, what WatchHistoryByPath polls).
type fakeHistory struct {
	agent.SessionHistory
	getSessionFunc       func(workDir, id string) (*agent.Session, error)
	getSessionByPathFunc func(path string) (*agent.Session, error)
}

func (h *fakeHistory) GetSession(workDir, id string) (*agent.Session, error) {
	if h.getSessionFunc == nil {
		return nil, nil
	}
	return h.getSessionFunc(workDir, id)
}

func (h *fakeHistory) GetSessionByPath(path string) (*agent.Session, error) {
	if h.getSessionByPathFunc == nil {
		return nil, nil
	}
	return h.getSessionByPathFunc(path)
}

// TestEngineReader_WatchSession_TransientErrorDoesNotTerminate: a failing
// read (the transcript momentarily unreadable) is retried on the next tick,
// not fatal.
func TestEngineReader_WatchSession_TransientErrorDoesNotTerminate(t *testing.T) {
	var calls int
	var mu sync.Mutex
	hist := &fakeHistory{getSessionFunc: func(_, _ string) (*agent.Session, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= 2 {
			return nil, errors.New("transcript not ready")
		}
		return sessionWith("recovered"), nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs, err := NewEngineReader(hist, "/proj").WatchSession(ctx, "s1")
	require.NoError(t, err)
	get, done := collectWatch(events)

	waitForWatch(t, get, 1, 2*time.Second)
	cancel()
	<-done
	require.NoError(t, <-errs)

	// The first streamed event is the entry that appeared after recovery,
	// proving the two earlier errors did not terminate the stream.
	assert.Equal(t, "recovered", get()[0].Entry.Content)
}

// collectWatch drains events into a slice (goroutine-safe getter) so tests can
// poll for progress while the watcher runs.
func collectWatch(events <-chan *WatchEvent) (get func() []*WatchEvent, done <-chan struct{}) {
	var mu sync.Mutex
	var got []*WatchEvent
	d := make(chan struct{})
	go func() {
		defer close(d)
		for ev := range events {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}
	}()
	return func() []*WatchEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]*WatchEvent(nil), got...)
	}, d
}

// waitForWatch polls the getter until at least n events arrived or the
// deadline passes.
func waitForWatch(t *testing.T, get func() []*WatchEvent, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for len(get()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d events; got %d", n, len(get()))
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWatchHistoryByPath_StreamsEntriesAndBoundary: the by-path watcher polls
// GetSessionByPath with the given path and emits the same entry/boundary
// vocabulary as the gRPC watch — one contract, two locators.
func TestWatchHistoryByPath_StreamsEntriesAndBoundary(t *testing.T) {
	var mu sync.Mutex
	gotPaths := map[string]int{}
	hist := &fakeHistory{getSessionByPathFunc: func(path string) (*agent.Session, error) {
		mu.Lock()
		gotPaths[path]++
		mu.Unlock()
		return sessionWith("a", "b"), nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := WatchHistoryByPath(ctx, hist, "/harp/persist/t.jsonl", time.Millisecond)
	get, done := collectWatch(events)

	// Two entries, then the stall boundary [0,2).
	waitForWatch(t, get, 3, 2*time.Second)
	cancel()
	<-done
	require.NoError(t, <-errs)

	got := get()
	assert.Equal(t, "a", got[0].Entry.Content)
	assert.Equal(t, "b", got[1].Entry.Content)
	b := got[2].Boundary
	require.NotNil(t, b, "growth stall must emit a boundary")
	assert.Equal(t, 0, b.FromIndex)
	assert.Equal(t, 2, b.ToIndex)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, gotPaths, 1, "the watcher must poll exactly the given path")
	assert.Positive(t, gotPaths["/harp/persist/t.jsonl"])
}

// TestWatchHistoryByPath_TransientErrorDoesNotTerminate: a failing read (e.g.
// the transcript momentarily unreadable) is retried on the next tick, not
// fatal — mirroring every watcher's fault posture.
func TestWatchHistoryByPath_TransientErrorDoesNotTerminate(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	hist := &fakeHistory{getSessionByPathFunc: func(string) (*agent.Session, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= 2 {
			return nil, errors.New("transcript not ready")
		}
		return sessionWith("recovered"), nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := WatchHistoryByPath(ctx, hist, "/p/t.jsonl", time.Millisecond)
	get, done := collectWatch(events)

	waitForWatch(t, get, 1, 2*time.Second)
	cancel()
	<-done
	require.NoError(t, <-errs)
	assert.Equal(t, "recovered", get()[0].Entry.Content)
}

// TestWatchHistoryByPath_CancelClosesChannels: cancelling the context ends the
// stream cleanly — both channels close, no error.
func TestWatchHistoryByPath_CancelClosesChannels(t *testing.T) {
	hist := &fakeHistory{getSessionByPathFunc: func(string) (*agent.Session, error) {
		return sessionWith(), nil // always idle
	}}

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := WatchHistoryByPath(ctx, hist, "/p/t.jsonl", time.Millisecond)
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				require.NoError(t, <-errs)
				return
			}
		case <-deadline:
			t.Fatal("events channel did not close after context cancel")
		}
	}
}

// --- client WatchSession: server stream → channels ---

// The canonical watcher shares WatchHistoryByPath's whole lifecycle contract
// and differs only in WHICH reader it polls, so its fault posture is pinned the
// same way: an unreadable transcript is warned and retried on the next tick.
// A long-lived stream must not die because the file is not there yet — the
// capture writer creates it lazily on the first successful record.
func TestWatchCanonicalTranscript_UnreadableFileDoesNotTerminate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-written", "transcript.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, errs := WatchCanonicalTranscript(ctx, missing, "swift-amber-falcon", time.Millisecond)

	// Give the loop several ticks to fail and retry, then prove it is still
	// alive by cancelling it: a terminated stream would already be closed.
	time.Sleep(20 * time.Millisecond)
	cancel()
	for range events { //nolint:revive // draining to closure is the assertion
	}
	require.NoError(t, <-errs, "a read failure is warned, never delivered as a stream-ending error")
}

// Cancelling the context ends the canonical stream cleanly: both channels
// close and no error is reported.
func TestWatchCanonicalTranscript_CancelClosesChannels(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "transcript.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := WatchCanonicalTranscript(ctx, missing, "swift-amber-falcon", time.Millisecond)
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				require.NoError(t, <-errs)
				return
			}
		case <-deadline:
			t.Fatal("events channel did not close after context cancel")
		}
	}
}

// poll <= 0 must fall back to the package default rather than panicking in
// time.NewTicker — the two watchers share that guard, and every caller that
// omits a cadence depends on it.
func TestWatchers_NonPositivePollUsesTheDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hist := &fakeHistory{getSessionByPathFunc: func(string) (*agent.Session, error) {
		return sessionWith(), nil
	}}
	byPathEvents, byPathErrs := WatchHistoryByPath(ctx, hist, "/p/t.jsonl", 0)
	canonEvents, canonErrs := WatchCanonicalTranscript(ctx, filepath.Join(t.TempDir(), "t.jsonl"), "h", -1)

	cancel()
	for range byPathEvents { //nolint:revive // draining to closure
	}
	for range canonEvents { //nolint:revive // draining to closure
	}
	require.NoError(t, <-byPathErrs)
	require.NoError(t, <-canonErrs)
}

// TestSessionWatcher_ShrunkTranscriptResyncsInsteadOfWedging: a transcript that
// gets rewritten, compacted or rotated comes back SHORTER than the high-water
// mark. Two things must survive that: no boundary may name indices the
// transcript no longer has (a consumer slices entries[from:to] on them), and
// subsequent growth must still stream. Before this was handled the marks stayed
// at the old high-water mark, so the watcher emitted an out-of-range boundary
// once and then heartbeated forever while the transcript grew underneath it.
func TestSessionWatcher_ShrunkTranscriptResyncsInsteadOfWedging(t *testing.T) {
	w := &sessionWatcher{heartbeatEvery: watchHeartbeatEvery}

	require.Len(t, w.step(sessionWith("a", "b", "c")), 3)

	// The transcript is rewritten down to a single entry.
	for _, ev := range w.step(sessionWith("x")) {
		if b := ev.Boundary; b != nil {
			assert.LessOrEqual(t, b.ToIndex, 1,
				"boundary must not name entries the shrunken transcript no longer has")
		}
	}

	// It then grows again. The new entry must stream rather than being
	// swallowed because the stale high-water mark is still ahead of it.
	grown := w.step(sessionWith("x", "y"))
	require.Len(t, grown, 1, "growth after a shrink must still stream")
	assert.Equal(t, "y", grown[0].Entry.Content)
}
