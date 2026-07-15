package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Recorder appends one canonical JSONL line per agent.ChatEvent to a harp's
// transcript.acp.jsonl. It is the type S2 tees the host-side ChatEvent stream
// through (internal/lm/grpc/chat.go's GRPCClient.Chat and
// internal/agentcoord/coord/enginehost.go's adapt) — this package only
// defines the writer; nothing in S1 wires it to those call sites yet.
type Recorder interface {
	// Record appends one canonical line for ev, stamping harp/engine/seq/ts.
	// Returns an error for a zero-value ev (no ChatEvent variant set) instead
	// of writing a blank line. Safe for concurrent use.
	Record(ev agent.ChatEvent) error
	// Close flushes and closes the underlying file, if one was ever opened.
	// Safe to call multiple times and safe to call when Record was never
	// called (see NewRecorder's empty-input note).
	Close() error
}

// fileRecorder is the on-disk Recorder: append-only, lazily-opened, one
// harp/engine pair per instance.
type fileRecorder struct {
	harp   string
	engine string
	path   string
	now    func() time.Time

	mu        sync.Mutex
	file      *os.File // nil until the first successful Record call
	seq       int
	sessionID string // latched from the first KindSession line seen
}

// NewRecorder returns a Recorder for harp/engine, targeting
// paths.HarpCanonicalTranscriptPath(harp).
//
// The underlying file is opened LAZILY, on the first successful Record call —
// NOT eagerly here — and the persist/ directory is created at that same
// moment. A chat that produces zero ChatEvents (Setup fails before any event,
// a cancelled turn, etc.) therefore leaves NO transcript.acp.jsonl at all,
// rather than a zero-byte file that could be mistaken for "captured, and
// genuinely empty." This is a deliberate departure from a naive
// open-immediately reading of the design sketch, made to satisfy the
// project's empty-input discipline (memory "silent-no-op-failure-mode": ask
// of every writer whether empty input fails or succeeds writing nothing —
// here it succeeds writing NOTHING, verifiably, by leaving no file). A
// consumer (S3's CanonicalHistory) can therefore treat file-absent as
// "nothing was ever recorded for this harp" without needing to also handle a
// present-but-empty file as a separate case.
//
// Seq starts at 0 on the first Record call and increases by 1, with no gaps,
// for the lifetime of the Recorder.
func NewRecorder(harp, engine string) (Recorder, error) {
	if harp == "" {
		return nil, fmt.Errorf("transcript: NewRecorder requires a non-empty harp")
	}
	if engine == "" {
		return nil, fmt.Errorf("transcript: NewRecorder requires a non-empty engine")
	}
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	if err != nil {
		return nil, fmt.Errorf("transcript: resolve canonical transcript path for harp %q: %w", harp, err)
	}
	return &fileRecorder{
		harp:   harp,
		engine: engine,
		path:   p,
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// Record implements Recorder.
func (r *fileRecorder) Record(ev agent.ChatEvent) error {
	kind, entry, session, complete, permission, err := payloadFromChatEvent(ev)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if kind == KindSession && session != nil {
		if sid := chatEventSessionID(ev); sid != "" {
			r.sessionID = sid
		}
	}

	rec := Record{
		V:          SchemaVersion,
		Harp:       r.harp,
		SessionID:  r.sessionID,
		Engine:     r.engine,
		Seq:        r.seq,
		TS:         r.now(),
		Kind:       kind,
		Entry:      entry,
		Session:    session,
		Complete:   complete,
		Permission: permission,
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("transcript: marshal record: %w", err)
	}
	line = append(line, '\n')

	if r.file == nil {
		if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
			return fmt.Errorf("transcript: create persist dir: %w", err)
		}
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("transcript: open %s: %w", r.path, err)
		}
		r.file = f
	}

	if _, err := r.file.Write(line); err != nil {
		return fmt.Errorf("transcript: write %s: %w", r.path, err)
	}
	r.seq++
	return nil
}

// Close implements Recorder.
func (r *fileRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Tee wraps events with a passthrough goroutine that calls rec.Record on every
// event before forwarding it unchanged, and returns the forwarding channel.
// This is the exact shape S2 drops in at its two host seams
// (GRPCClient.Chat's returned events channel, and coord/enginehost.adapt's
// consumed `out`) — see docs/transcript-schema.md and the tough-cloud plan
// §2c. Recording happens BEFORE forwarding so a consumer that stops reading
// early never causes an event to be forwarded-but-not-recorded.
//
// A Record error is deliberately swallowed except for a debug-log style
// callback-free drop: capture must never be able to break or stall the live
// chat it is shadowing (a transcript write failure is not a reason to lose a
// user's conversation). Record errors are therefore recorded as best-effort;
// S2, which owns the actual host wiring, decides whether to surface them
// (e.g. via a logger) when it wires Tee in — this helper's contract is just
// "never blocks, never drops an event from the forwarded stream, never panics
// the caller's chat on a write failure."
func Tee(rec Recorder, events <-chan agent.ChatEvent) <-chan agent.ChatEvent {
	out := make(chan agent.ChatEvent)
	go func() {
		defer close(out)
		for ev := range events {
			_ = rec.Record(ev) // best-effort; see doc comment
			out <- ev
		}
	}()
	return out
}
