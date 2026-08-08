package transcript

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Recorder appends one canonical JSONL line per agent.ChatEvent to a harp's
// transcript.jsonl. It is the type S2 tees the host-side ChatEvent stream
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
	policy RawPolicy

	// open creates/appends the transcript file. A seam, not a strategy: the
	// only production implementation is openAppendFile, and it exists so a
	// test can drive the partial-write path — a Write that delivers SOME
	// bytes and then fails — which a real *os.File on a healthy filesystem
	// will not produce on demand.
	open func(path string) (io.WriteCloser, error)

	mu        sync.Mutex
	file      io.WriteCloser // nil until the first successful Record call
	seq       int
	sessionID string // latched from the first KindSession line seen

	// failures counts events handed to Record that were NOT captured, and
	// warned/closeWarned keep that observable without flooding.
	// Every live capture seam — tee, TeeAndClose, RecordUserText,
	// CoordinatedRecorder — deliberately discards Record's error so a
	// transcript failure can never perturb the chat it shadows. That is the
	// right call, but with nothing behind it the whole class ("full disk /
	// EACCES / bad path") produced a green chat, zero captured bytes and not
	// one diagnostic. The recorder is the only party that knows, so the
	// recorder is what says so.
	failures    int
	warned      bool
	closeWarned bool
}

// RecorderOption configures optional NewRecorder behavior. Additive: every
// existing NewRecorder(harp, engine) call site keeps compiling and behaving
// identically (RawLossyOnly, the default, was always the intended behavior
// for the ONLY thing an option can change so far).
type RecorderOption func(*fileRecorder)

// WithRawPolicy sets the RawPolicy governing whether/when Record persists a
// ChatEvent's IR3 raw side channel into Record.Raw (see RawPolicy's doc
// comment). An empty/unrecognized policy normalizes to DefaultRawPolicy
// (RawLossyOnly) rather than silently behaving like RawOff.
func WithRawPolicy(p RawPolicy) RecorderOption {
	return func(r *fileRecorder) { r.policy = normalizeRawPolicy(p) }
}

// NewRecorder returns a Recorder for harp/engine, targeting
// paths.HarpCanonicalTranscriptPath(harp).
//
// The underlying file is opened LAZILY, on the first successful Record call —
// NOT eagerly here — and the persist/ directory is created at that same
// moment. A chat that produces zero ChatEvents (Setup fails before any event,
// a cancelled turn, etc.) therefore leaves NO transcript.jsonl at all,
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
func NewRecorder(harp, engine string, opts ...RecorderOption) (Recorder, error) {
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
	r := &fileRecorder{
		harp:   harp,
		engine: engine,
		path:   p,
		now:    func() time.Time { return time.Now().UTC() },
		policy: DefaultRawPolicy,
		open:   openAppendFile,
	}
	for _, opt := range opts {
		opt(r)
	}
	// NO CONTENT POLICY HERE, deliberately. A Recorder writes what it is
	// given, in full; filtering happens on the READ side
	// (grpc.NewFilteredSource). Storage stays total.
	//
	// An earlier revision wrapped this return in a filtering decorator. That
	// was wrong in a way worth naming, because it looked safe: it reasoned
	// that a filtered write is recoverable because the canonical transcript
	// is re-derivable from the engine's vendor log. That holds ONLY for
	// tier-1 sources. A tier-2 source (a raw stream teed from an engine with
	// no vendor log — generic ACP, 0.8) is GONE once transformed, so this
	// file is its only copy and a write-time filter would destroy those bytes
	// permanently. A constructor named NewRecorder is also the wrong place to
	// hide a policy decision its callers never asked for.
	return r, nil
}

// rawToPersist decides what (if anything) of ev.Raw this recorder's policy
// keeps for kind. See RawPolicy's doc comment for the per-policy rule.
func (r *fileRecorder) rawToPersist(ev agent.ChatEvent, kind Kind) json.RawMessage {
	if len(ev.Raw) == 0 || r.policy == RawOff {
		return nil
	}
	if r.policy == RawAll || kind == KindRaw {
		return ev.Raw
	}
	return nil // RawLossyOnly, and Raw only SUPPLEMENTS another payload
}

// Record implements Recorder. Every failure is also counted and (once)
// warned about — see fileRecorder.failures and noteFailure — because the
// callers that swallow this error are structurally unable to report it.
func (r *fileRecorder) Record(ev agent.ChatEvent) error {
	err := r.record(ev)
	if err != nil {
		r.noteFailure(err)
	}
	return err
}

// noteFailure counts one uncaptured event and warns on the first one only:
// a failing disk must surface, but it must not turn one broken chat into N
// identical warnings. Close reports the running total.
func (r *fileRecorder) noteFailure(err error) {
	r.mu.Lock()
	first := !r.warned
	r.warned = true
	r.failures++
	r.mu.Unlock()

	if first {
		clidiag.Warn("ctxloom", "transcript capture failed for harp %q (%s): %v — this chat continues, but its canonical transcript is incomplete; further failures are counted, not repeated", r.harp, r.path, err)
	}
}

// openAppendFile is the production opener: create-if-absent, append-only.
func openAppendFile(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// ensureFile lazily creates the persist dir and opens the append-only file.
// Extracted from record so the write path stays under the project's
// complexity gate.
func (r *fileRecorder) ensureFile() error {
	if r.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("transcript: create persist dir: %w", err)
	}
	f, err := r.open(r.path)
	if err != nil {
		return fmt.Errorf("transcript: open %s: %w", r.path, err)
	}
	r.file = f
	return nil
}

func (r *fileRecorder) record(ev agent.ChatEvent) error {
	kind, entry, session, complete, permission, err := payloadFromChatEvent(ev)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	raw := r.rawToPersist(ev, kind)
	if kind == KindRaw && raw == nil {
		// RawOff suppressed the ONLY payload this event had: nothing left to
		// write. A deliberate, policy-directed skip — not the silent-no-op
		// bug pattern this package is otherwise paranoid about — mirroring
		// NewRecorder's own "write nothing, verifiably" convention for a
		// chat that produced no events at all.
		return nil
	}

	if kind == KindSession && session != nil {
		if sid := ev.Session.SessionID; sid != "" {
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
		Raw:        raw,
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("transcript: marshal record: %w", err)
	}
	line = append(line, '\n')

	if err := r.ensureFile(); err != nil {
		return err
	}

	n, werr := r.file.Write(line)
	if werr != nil {
		if n > 0 {
			r.salvagePartialLine()
		}
		return fmt.Errorf("transcript: write %s: %w", r.path, werr)
	}
	r.seq++
	return nil
}

// salvagePartialLine handles a write that delivered SOME of a line and then
// failed. Two things have to happen, and neither is optional.
//
// First the fragment is terminated with a newline: it has no trailing one of
// its own, so the next record's bytes would otherwise be appended to it and
// the two would collapse into a single unparseable line — losing a record
// that was written perfectly, on top of the one that was not. Bounding the
// damage to one line is what makes the reader's degrade-to-partial contract
// mean anything here.
//
// Then seq advances past the lost record. Seq's documented purpose is that a
// reader can detect truncation by a discontinuity; reusing the seq would put
// the fragment and its successor at the SAME ordering key, leaving the file
// looking continuous at precisely the moment it is not. The gap is the
// evidence.
//
// The terminating write is best-effort: if the filesystem is refusing bytes,
// it will refuse this one too, and there is nothing further to be done about
// a fragment that cannot be closed off.
func (r *fileRecorder) salvagePartialLine() {
	_, _ = r.file.Write([]byte("\n"))
	r.seq++
}

// Close implements Recorder.
func (r *fileRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Report the total ONCE, even when no file was ever opened — the
	// "couldn't create the dir at all" case is precisely the one that leaves
	// zero bytes and would otherwise pass for a chat that simply had nothing
	// to record.
	if r.failures > 0 && !r.closeWarned {
		r.closeWarned = true
		clidiag.Warn("ctxloom", "transcript capture for harp %q lost %d event(s); %s is incomplete or absent", r.harp, r.failures, r.path)
	}
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// RecordUserText appends one `user` canonical entry for text to rec — the
// case tee/TeeAndClose structurally cannot reach, because they only ever
// wrap the OUTBOUND ChatEvent stream. Text entered by the caller travels the
// INBOUND ChatMessage channel instead, so without an explicit tap at each
// host seam (GRPCClient.Chat's `in` pump, coord/enginehost.go's SetTurnSink
// and briefing sends) a structured session's canonical transcript carries
// assistant output but no user turns at all. Mirrors the `user`
// entry oneshot.RecordOneshot synthesizes for the oneshot-Execute regime.
//
// rec may be nil (no harp / capture disabled) and text may be empty (a
// permission answer or turn-cancel control message, or a genuinely blank
// turn) — both are silent no-ops, the same empty-input discipline every
// other capture path in this package follows. A Record error is swallowed
// for the same reason tee swallows its own: transcript capture must never be
// visible to, or perturb, the live chat it shadows.
func RecordUserText(rec Recorder, text string) {
	if rec == nil || text == "" {
		return
	}
	_ = rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeUser,
		Content: text,
	}})
}

// tee wraps events with a passthrough goroutine that calls rec.Record on every
// event before forwarding it unchanged, and returns the forwarding channel.
// This is the exact shape S2 drops in at its two host seams
// (GRPCClient.Chat's returned events channel, and coord/enginehost.adapt's
// consumed `out`) — see docs/transcript-schema.md §2c. Recording happens BEFORE forwarding so a consumer that stops reading
// early never causes an event to be forwarded-but-not-recorded. Unexported:
// TeeAndClose is the only production shape any host seam needs (a bare tee
// would leak the Recorder's fd), so this stays a package-private helper
// (no external caller; only TeeAndClose and this package's tests
// used the exported name).
//
// A Record error is deliberately swallowed except for a debug-log style
// callback-free drop: capture must never be able to break or stall the live
// chat it is shadowing (a transcript write failure is not a reason to lose a
// user's conversation). Record errors are therefore recorded as best-effort;
// S2, which owns the actual host wiring, decides whether to surface them
// (e.g. via a logger) when it wires tee in.
//
// The contract is: CAPTURE never delays the stream, no event is dropped from
// the forwarded stream, and a write failure never panics the caller's chat.
// It is NOT "never blocks", and cannot be: out is unbuffered, so the forward
// applies exactly the backpressure the caller's own consumer applies, which
// is the price of losing no events — the two guarantees are mutually
// exclusive on an unbuffered channel and only the lossless one is chosen
// here. What capture buys is ordering: Record runs BEFORE the send, so a slow
// or absent consumer delays forwarding but never a write.
//
// The consequence a caller must own: this goroutine lives until events closes
// AND every event has been taken from out. A consumer that abandons out
// leaves it parked forever, and via TeeAndClose that also leaves the
// Recorder's file handle open. There is no cancellation seam here today.
func tee(rec Recorder, events <-chan agent.ChatEvent) <-chan agent.ChatEvent {
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

// TeeAndClose is tee plus automatic Recorder cleanup: it wraps events exactly
// like tee, and once the source channel is fully drained (so tee's internal
// goroutine has recorded and forwarded every event and closed its own output),
// calls rec.Close(). This is the shape S2's host seams actually need —
// GRPCClient.Chat and coord/enginehost.adapt own no separate "the chat is
// over" signal of their own beyond the events channel closing, so a bare tee
// would leak the Recorder's open file handle for the lifetime of the process.
// Close's error is swallowed for the same reason tee swallows Record's: a
// transcript-capture failure must never be visible to (or block) the live
// chat it is shadowing.
//
// "Fully drained" is literal and is the caller's obligation: Close runs only
// after every event has been taken from the returned channel. A consumer that
// stops reading part-way parks both goroutines and the file handle stays open
// for the lifetime of the process — the same fd leak a bare tee would cause,
// moved rather than removed. See tee's doc for why no cancellation seam
// exists.
func TeeAndClose(rec Recorder, events <-chan agent.ChatEvent) <-chan agent.ChatEvent {
	teed := tee(rec, events)
	out := make(chan agent.ChatEvent)
	go func() {
		defer close(out)
		defer func() { _ = rec.Close() }()
		for ev := range teed {
			out <- ev
		}
	}()
	return out
}
