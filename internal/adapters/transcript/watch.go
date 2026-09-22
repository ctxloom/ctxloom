package transcript

import (
	"context"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file is the live watch: a structured, turn-based view of a
// transcript so a frontend renders a native chat UI (messages, not raw pty
// bytes). It is purely additive over whole-transcript reassembly — the
// watcher polls a reader and diffs against a high-water mark — so every
// reader gets it for free.

// WatchEvent is one event of a live transcript watch: exactly one of a new
// Entry, a Boundary closing a response, or a Heartbeat keeping the stream
// warm.
type WatchEvent struct {
	Entry     *agent.SessionEntry
	Boundary  *ResponseBoundary
	Heartbeat bool
}

// ResponseBoundary marks a completed response: the entries [FromIndex,
// ToIndex) of the transcript.
type ResponseBoundary struct {
	FromIndex int
	ToIndex   int
}

const (
	// defaultWatchPoll is how often a watcher re-reads the transcript. It also
	// serves as the settle interval: a single poll with no growth is treated as a
	// stopping point (response complete).
	defaultWatchPoll = 250 * time.Millisecond

	// watchHeartbeatEvery emits a heartbeat once every N fully-idle polls (no new
	// entries, nothing pending a boundary) so the stream stays warm without
	// spamming keepalives. At the default poll that is ~one heartbeat every 2s.
	watchHeartbeatEvery = 8
)

// sessionWatcher tracks the high-water marks across successive polls of one
// transcript and turns each poll into the WatchEvents to emit. It is the pure
// core of every watcher: given the latest reassembled session it decides what is
// new, when a response has just completed, and when to keep the stream warm —
// with no timers or I/O, so it is exhaustively unit-testable.
type sessionWatcher struct {
	sent           int // entries already streamed as `entry` events
	lastBoundary   int // start index of the response not yet closed by a boundary
	idleTicks      int // consecutive fully-idle polls (for heartbeat cadence)
	heartbeatEvery int // emit a heartbeat every N idle polls (<=0 = every poll)
}

// step consumes the latest reassembled session and returns the events to emit:
//   - new entries (entries[sent:len]) stream as `entry` events when the
//     transcript grew;
//   - otherwise, if material has accumulated since the last boundary, the stall
//     is a stopping point and a `boundary` marking [lastBoundary, sent) is emitted;
//   - otherwise the session is fully idle and a `heartbeat` is emitted on the
//     configured slower cadence.
func (w *sessionWatcher) step(sess *agent.Session) []*WatchEvent {
	n := 0
	if sess != nil {
		n = len(sess.Entries)
	}

	// A transcript that was rewritten, compacted or rotated comes back SHORTER
	// than the high-water mark. Both marks index into the transcript, so they
	// must be clamped to what it actually holds now — otherwise a boundary
	// names entries the consumer cannot slice, and every mark stays permanently
	// ahead of the entry count, so no later growth is ever recognized as new.
	// Clamping (rather than replaying from zero) keeps the emitted indices
	// valid without duplicating entries on a stream that has no reset event.
	if n < w.sent {
		w.sent = n
		if w.lastBoundary > n {
			w.lastBoundary = n
		}
	}

	if n > w.sent {
		events := make([]*WatchEvent, 0, n-w.sent)
		for i := w.sent; i < n; i++ {
			e := sess.Entries[i]
			events = append(events, &WatchEvent{Entry: &e})
		}
		w.sent = n
		w.idleTicks = 0
		return events
	}

	// No growth this pass.
	if w.sent > w.lastBoundary {
		// Material accumulated since the last boundary and growth stalled: the
		// just-completed response is entries[lastBoundary, sent).
		boundary := &WatchEvent{Boundary: &ResponseBoundary{FromIndex: w.lastBoundary, ToIndex: w.sent}}
		w.lastBoundary = w.sent
		w.idleTicks = 0
		return []*WatchEvent{boundary}
	}

	// Fully idle: nothing new and nothing pending a boundary.
	w.idleTicks++
	if w.heartbeatEvery <= 0 || w.idleTicks%w.heartbeatEvery == 0 {
		return []*WatchEvent{{Heartbeat: true}}
	}
	return nil
}

// WatchHistoryByPath streams a transcript's structured turns by HOST file
// path, polling the engine's normalized GetSessionByPath parser. It exists
// for transcripts bound by LOCATION rather than by the SessionStart hook
// (sessions.LocateTranscript): a containerized child's transcript lives in
// ctxloom's own per-harp store (~/.ctxloom/sessions/<harp>/persist/…), which
// the engine's own project-scoped store lookup never finds — the host owns
// that file, so the host parses it. The same sessionWatcher core as
// EngineReader.WatchSession decides what to emit, so both feeds speak one
// contract. poll <= 0 uses the default cadence. A transient read error is
// warned and retried next tick (a long-lived stream must not die on a blip);
// the stream ends when ctx is cancelled, after which both channels close
// (errs carries a fatal stream error, so consumers render either source
// identically).
func WatchHistoryByPath(ctx context.Context, hist agent.SessionHistory, path string, poll time.Duration) (<-chan *WatchEvent, <-chan error) {
	return pollTranscript(ctx, "watch transcript", path, poll, func() (*agent.Session, error) {
		return hist.GetSessionByPath(path)
	})
}

// WatchCanonicalTranscript streams a captured transcript.jsonl's structured
// turns by polling ParseTranscriptFile — the canonical counterpart to
// WatchHistoryByPath (which polls a legacy per-engine file). `session
// transcript watch` prefers this over both EngineReader.WatchSession (an
// engine session id) and WatchHistoryByPath (a located legacy transcript)
// whenever the harp has a canonical transcript, since ctxloom's own capture
// is available host-side regardless of where the engine ran. harpName is
// both the file's lookup key and the resulting Session.ID; poll <= 0 uses
// the default cadence. Same fault-tolerance and lifecycle contract as
// WatchHistoryByPath: a transient parse error is warned and retried next
// tick, never kills the stream; it ends when ctx is cancelled.
func WatchCanonicalTranscript(ctx context.Context, path, harpName string, poll time.Duration) (<-chan *WatchEvent, <-chan error) {
	return pollTranscript(ctx, "watch canonical transcript", path, poll, func() (*agent.Session, error) {
		return ParseTranscriptFile(path, harpName)
	})
}

// pollTranscript is the host-side polling feed every watcher is: read
// the whole conversation, hand it to the shared sessionWatcher core to decide
// what is new, emit that. Only WHICH reader runs (and the label its failures
// are warned under) differs between the two locators, so the lifecycle is
// stated once here — poll <= 0 falls back to the default cadence, a read
// failure is warned and retried on the next tick rather than ending a
// long-lived stream, and ctx cancellation closes both channels. errs mirrors
// the gRPC watch signature so a consumer renders either source identically.
func pollTranscript(ctx context.Context, label, path string, poll time.Duration, read func() (*agent.Session, error)) (<-chan *WatchEvent, <-chan error) {
	if poll <= 0 {
		poll = defaultWatchPoll
	}
	events := make(chan *WatchEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		w := &sessionWatcher{heartbeatEvery: watchHeartbeatEvery}
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			sess, err := read()
			if err != nil {
				clidiag.Warn("ctxloom", "%s %s: %v", label, path, err)
			} else {
				for _, ev := range w.step(sess) {
					select {
					case events <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return events, errs
}
