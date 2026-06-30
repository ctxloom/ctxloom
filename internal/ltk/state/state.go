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
// concurrent hook invocations (agents can issue parallel tool calls) may race
// the read-modify-write and lose an arm, but Save writes atomically (temp file
// + rename), so a reader never sees a torn file. Losing an arm fails safe — the
// denial just repeats and re-arms.
type Store struct {
	fs      afero.Fs
	path    string
	Pending map[string]pending `json:"pending"` // command → armed override band
}

// Open loads the store at path from fs. A missing or corrupt file yields an
// empty store (best-effort: an unreadable state file must never break
// evaluation). A nil fs defaults to the real OS filesystem.
func Open(fs afero.Fs, path string) *Store {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	s := &Store{fs: fs, path: path, Pending: map[string]pending{}}
	if b, err := afero.ReadFile(fs, path); err == nil {
		_ = json.Unmarshal(b, s)
		if s.Pending == nil {
			s.Pending = map[string]pending{}
		}
	}
	return s
}

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
