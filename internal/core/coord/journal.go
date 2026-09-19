package coord

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Fact is one durable, append-only record in a journal. Folds take time from
// At (fact-carried time) — fold logic never calls time.Now(), so replay is
// deterministic.
type Fact struct {
	Kind string          `json:"kind"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data,omitempty"`
}

// decode unmarshals a fact payload into out; folds use it and IGNORE unknown
// kinds (forward compatibility: an older binary replaying a newer journal
// keeps every fact it understands).
func (f Fact) decode(out any) error { return json.Unmarshal(f.Data, out) }

// fold is one projection over exactly one journal. Apply is only ever called
// from the store's single writer goroutine (replay, then live appends), so
// fold implementations need no locking of their own; reads go through
// Store.View/Store.Exec which exclude the writer.
type fold interface {
	apply(Fact)
}

// Store binds one JSONL journal file to its folds under the journal
// discipline:
//
//   - exactly one writer goroutine per journal; every append is
//     append-then-apply under that serialization;
//   - fsync before any durability-asserting response (Exec does not return
//     until the facts are synced AND applied);
//   - replay truncates a torn tail (a final line that is incomplete or
//     unparseable); corruption anywhere else fails loudly;
//   - folds own ALL mutable state — command handlers produce facts inside
//     Exec's decide and never mutate a projection directly.
type Store struct {
	path  string
	f     *os.File
	folds []fold

	mu   sync.RWMutex // held for write during apply; View readers take RLock
	reqs chan storeReq
	done chan struct{}

	closeOnce sync.Once
}

type storeReq struct {
	decide func() ([]Fact, error)
	reply  chan error
}

// errStoreClosed answers Exec on a closed store.
var errStoreClosed = errors.New("coord: store closed")

// openStore opens (creating if needed, 0600 file / 0700 dir) the journal at
// path, replays it through the folds (truncating a torn tail), and starts the
// single writer goroutine.
func openStore(path string, folds ...fold) (*Store, error) {
	return openStoreFromOffset(path, 0, folds...)
}

// openStoreFromOffset is openStore's snapshot-aware variant (D4 CHECKPOINT
// compaction): replay starts at fromOffset instead of byte 0 — the caller
// has already restored the folds' state up to that point from a snapshot
// (checkpoint.go), so re-parsing the journal's head would be redundant work
// on a long-running session's large items.jsonl. fromOffset is NEVER
// trusted blindly: out of [0, file size] (a stale snapshot pointing past a
// shorter/rewritten file) falls back to a full replay from 0 — safe by
// construction, since the snapshot is purely an additive replay shortcut
// and the journal remains the sole source of truth (journal truncation
// itself stays deferred to Wave E's retention decision).
func openStoreFromOffset(path string, fromOffset int64, folds ...fold) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("coord: journal dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("coord: open journal %s: %w", path, err)
	}
	s := &Store{
		path:  path,
		f:     f,
		folds: folds,
		reqs:  make(chan storeReq),
		done:  make(chan struct{}),
	}
	if fi, serr := f.Stat(); serr == nil && (fromOffset < 0 || fromOffset > fi.Size()) {
		fromOffset = 0
	}
	if err := s.replay(fromOffset); err != nil {
		_ = f.Close()
		return nil, err
	}
	go s.writer()
	return s, nil
}

// Offset returns the journal's current write position — the value a D4
// CHECKPOINT snapshot (checkpoint.go) records alongside the folds' state, so
// a later openStoreFromOffset can skip straight to the tail.
func (s *Store) Offset() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.f.Seek(0, io.SeekCurrent)
}

// OffsetView is Offset and View fused into ONE locked window: it reads the
// journal's current write position and runs read with the folds quiescent,
// both under the SAME RLock acquisition, so a concurrent Exec cannot land
// between the two. A caller that instead calls Offset() and View()
// as two separate calls — as checkpoint.go's writeItemsSnapshot used to —
// leaves a gap a concurrent append can land in: the read then observes fold
// state NEWER than the recorded offset, so a later restore(snapshot) +
// openStoreFromOffset(offset) tail-replay re-applies the facts that landed in
// that gap on top of a fold that already includes them. Any snapshot/read
// pairing that must be internally consistent (i.e. "the state as of exactly
// this offset") MUST go through this method, not the two primitives
// separately.
func (s *Store) OffsetView(read func(offset int64)) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	offset, err := s.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	read(offset)
	return offset, nil
}

// replay reads every complete, parseable line from fromOffset forward and
// applies it to the folds. A torn tail — a final line without its newline,
// or a final line that does not parse (a crash mid-append) — is truncated
// away; an unparseable line that is NOT the tail is corruption and fails
// loudly (fail-loud philosophy: better an explicit finding than a silently
// wrong projection).
//
// It streams a line at a time: recovery memory is bounded by the LONGEST line,
// never by the file. Slurping the journal whole cost RAM proportional to its
// size on exactly the paths that exist to avoid that work — a missing, stale, or
// out-of-range checkpoint offset all fall back to replaying from byte 0, when
// the journal is at its largest.
func (s *Store) replay(fromOffset int64) error {
	if _, err := s.f.Seek(fromOffset, io.SeekStart); err != nil {
		return fmt.Errorf("coord: seek journal %s to %d: %w", s.path, fromOffset, err)
	}
	r := bufio.NewReaderSize(s.f, journalLineBuffer)
	good := fromOffset
	offset := fromOffset
	for {
		line, rerr := r.ReadBytes('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return fmt.Errorf("coord: read journal %s: %w", s.path, rerr)
		}
		if errors.Is(rerr, io.EOF) {
			if len(line) == 0 {
				return s.seekEnd(good)
			}
			// No newline: a torn final line. Truncate to the last good offset.
			return s.truncateTo(good)
		}
		var fact Fact
		if uerr := json.Unmarshal(line[:len(line)-1], &fact); uerr != nil {
			if restIsBlank(r) {
				// The unparseable line is the tail: torn append, truncate.
				return s.truncateTo(good)
			}
			return fmt.Errorf("coord: journal %s corrupt at offset %d (not a torn tail): %v", s.path, offset, uerr)
		}
		for _, fl := range s.folds {
			fl.apply(fact)
		}
		offset += int64(len(line))
		good = offset
	}
}

// journalLineBuffer sizes replay's read buffer. Longer lines still parse —
// bufio collects them across fills — this only sets how many reads that takes.
const journalLineBuffer = 64 << 10

// restIsBlank reports whether nothing but whitespace remains, which is what
// separates an unparseable line that is the TORN TAIL of a crashed append from
// corruption in the middle of the journal. It stops at the first non-space byte,
// so the corrupt case never reads the remainder.
func restIsBlank(r io.Reader) bool {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if len(bytes.TrimSpace(buf[:n])) != 0 {
			return false
		}
		if err != nil {
			return errors.Is(err, io.EOF)
		}
	}
}

func (s *Store) truncateTo(n int64) error {
	if err := s.f.Truncate(n); err != nil {
		return fmt.Errorf("coord: truncate torn journal tail %s: %w", s.path, err)
	}
	return s.seekEnd(n)
}

func (s *Store) seekEnd(n int64) error {
	if _, err := s.f.Seek(n, io.SeekStart); err != nil {
		return fmt.Errorf("coord: seek journal %s: %w", s.path, err)
	}
	return nil
}

// writer is the journal's single writer goroutine: it serializes every
// decide→append→fsync→apply window.
func (s *Store) writer() {
	for {
		select {
		case req := <-s.reqs:
			req.reply <- s.execLocked(req.decide)
		case <-s.done:
			// Drain requests racing the close so no Exec caller hangs.
			for {
				select {
				case req := <-s.reqs:
					req.reply <- errStoreClosed
				default:
					return
				}
			}
		}
	}
}

func (s *Store) execLocked(decide func() ([]Fact, error)) error {
	// The write lock makes the folds quiescent for decide's reads and for
	// apply's mutations; View readers are excluded for the whole window.
	s.mu.Lock()
	defer s.mu.Unlock()
	facts, err := decide()
	if err != nil {
		return err
	}
	if len(facts) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, fact := range facts {
		line, merr := json.Marshal(fact)
		if merr != nil {
			return fmt.Errorf("coord: marshal fact %s: %w", fact.Kind, merr)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if _, err := s.f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("coord: append journal %s: %w", s.path, err)
	}
	// Durability before any durability-asserting response — and before the
	// facts become visible in the folds.
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("coord: fsync journal %s: %w", s.path, err)
	}
	for _, fact := range facts {
		for _, fl := range s.folds {
			fl.apply(fact)
		}
	}
	return nil
}

// Exec runs decide under the journal's single-writer serialization. decide
// may read the folds (quiescent inside the window) and returns the facts to
// append; they are written, fsynced, and applied to every fold before Exec
// returns. Returning an error appends nothing.
func (s *Store) Exec(decide func() ([]Fact, error)) error {
	req := storeReq{decide: decide, reply: make(chan error, 1)}
	select {
	case s.reqs <- req:
		return <-req.reply
	case <-s.done:
		return errStoreClosed
	}
}

// View runs read with the folds quiescent (shared lock; concurrent Views are
// fine, appends are excluded). read must not retain references into fold
// state beyond the call.
func (s *Store) View(read func()) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	read()
}

// Close stops the writer and closes the journal file.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		defer s.mu.Unlock()
		err = s.f.Close()
	})
	return err
}
