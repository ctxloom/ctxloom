// Package state persists short-lived "confirm by repeating" tokens. When a
// command is denied, ltk remembers it briefly; running the exact same command
// again within the window counts as an explicit override and is allowed.
//
// This is an escape hatch, not a security control: a determined agent can just
// repeat the command. It exists so a human (or a deliberate agent) can get past
// a cooperative nudge without editing config, while every override is visible.
// Rules marked non-confirmable are never armed here, so repeating them never
// helps — see internal/rules.
//
// All file access goes through an afero.Fs so the store is testable against an
// in-memory filesystem.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	fs2 "io/fs"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// pending is a single armed override: the repeat is honored only in the band
// [NotBefore, Expiry] (unix seconds) — NotBefore enforces a minimum delay, Expiry
// the window. With no delay, NotBefore == the arm time.
type pending struct {
	NotBefore int64 `json:"not_before"`
	Expiry    int64 `json:"expiry"`
}

// Store is a tiny on-disk map of command → armed override. It is best-effort:
// concurrent hook invocations (agents can issue parallel tool calls) read the
// file, mutate their own copy, and write the WHOLE map back, so one invocation
// can overwrite another's update. Save writes atomically (temp file + rename),
// so a reader never sees a torn file — but atomicity of the write is not
// atomicity of the read-modify-write, and the two directions are not
// symmetric.
//
// A lost ARM fails safe: the denial simply repeats and re-arms, and nothing
// was permitted that the user did not ask for.
//
// A lost CLEAR fails OPEN, and that is the direction to keep in mind. An
// invocation that read the store before another consumed an override still
// holds that override in its snapshot, and its own Save writes it back — so a
// spent, deliberately-consumed override is resurrected by an entirely
// unrelated command's denial, and the next repeat of the first command is
// allowed with no fresh denial and no fresh delay. Concurrent repeats of the
// SAME command have the same shape: each reads "armed and ready" before any of
// them clears, so one confirmation admits several runs.
//
// This is not currently prevented; race_test.go pins both directions so the
// claim is maintained by the suite rather than asserted here. It is bounded by
// what the mechanism is for — an escape hatch whose cost is a deliberate
// repeat, not a security control — but "the user consented once and ltk acted
// on it twice" is a real gap, not a documented safety property.
type Store struct {
	fs      afero.Fs
	path    string
	loadErr error
	Pending map[string]pending `json:"pending"` // command → armed override band
}

// Open loads the store at path from fs. A missing or corrupt file yields an
// empty store (best-effort: an unreadable state file must never break
// evaluation). A nil fs defaults to the real OS filesystem.
//
// Open therefore cannot fail, but it can lose everything: a state file that
// does not decode discards EVERY live override, not just a damaged one, and
// the next Save overwrites the file wholesale so the evidence goes too. The
// decision that follows is unaffected — a lost override just means the denial
// repeats — but the operator gets no hint that their state file is unusable
// and their confirmations keep evaporating. LoadError reports it so a caller
// can say so, on the same channel Save's failures already ride.
func Open(fs afero.Fs, path string) *Store {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	s := &Store{fs: fs, path: path, Pending: map[string]pending{}}
	b, err := afero.ReadFile(fs, path)
	switch {
	case errors.Is(err, fs2.ErrNotExist):
		return s // no state yet is the ordinary first-run case, not a failure
	case err != nil:
		s.loadErr = fmt.Errorf("read %s: %w", path, err)
		return s
	}
	if err := json.Unmarshal(b, s); err != nil {
		s.Pending = map[string]pending{}
		s.loadErr = fmt.Errorf("decode %s: %w (every live override was discarded)", path, err)
		return s
	}
	if s.Pending == nil {
		s.Pending = map[string]pending{}
	}
	return s
}

// LoadError reports why the store came up empty, or nil when it loaded cleanly
// (an absent file is clean — it is the first-run case). It never affects a
// decision; it exists so the emptiness is attributable instead of silent.
func (s *Store) LoadError() error { return s.loadErr }

// Armed reports whether cmd has an unexpired pending entry — i.e. it was denied
// recently and an override is still live (the delay may or may not have elapsed;
// see Ready).
func (s *Store) Armed(cmd string, now time.Time) bool {
	p, ok := s.Pending[cmd]
	return ok && now.Unix() < p.Expiry
}

// Ready reports whether a live override for cmd may now be consumed: the delay
// has elapsed (now ≥ NotBefore) and the window has not (now < Expiry).
func (s *Store) Ready(cmd string, now time.Time) bool {
	p, ok := s.Pending[cmd]
	return ok && now.Unix() >= p.NotBefore && now.Unix() < p.Expiry
}

// RemainingDelay returns how many seconds remain before a live override for cmd
// becomes consumable, or 0 if the delay has already elapsed (or there is none).
func (s *Store) RemainingDelay(cmd string, now time.Time) int {
	if p, ok := s.Pending[cmd]; ok && now.Unix() < p.NotBefore {
		return int(p.NotBefore - now.Unix())
	}
	return 0
}

// Arm records cmd as pending: confirmable in the band [now+delay, now+window].
func (s *Store) Arm(cmd string, now time.Time, delay, window time.Duration) {
	s.Pending[cmd] = pending{
		NotBefore: now.Add(delay).Unix(),
		Expiry:    now.Add(window).Unix(),
	}
}

// Clear removes cmd's pending entry (call after consuming a confirmation).
func (s *Store) Clear(cmd string) { delete(s.Pending, cmd) }

// Save prunes expired entries and writes the store, creating its directory.
// The write is atomic (a sibling temp file renamed into place), so a concurrent
// reader sees either the old store or the new one, never a torn file.
func (s *Store) Save(now time.Time) error {
	for c, p := range s.Pending {
		if now.Unix() >= p.Expiry {
			delete(s.Pending, c)
		}
	}
	if err := s.fs.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write via shared/iox: a unique temp file in the destination dir is
	// written then renamed, so two concurrent Saves never rename each other's
	// half-written file and a reader never sees a torn one. (Parent dir created
	// just above, satisfying WriteFileAtomicFs's precondition.)
	return iox.WriteFileAtomicFs(s.fs, s.path, b, 0o644)
}
