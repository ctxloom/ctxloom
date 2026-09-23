package engine

import (
	"context"
	"errors"
	"fmt"
)

// A WAKE starts a turn in an idle session so that session's turn-start hook
// runs and hands it its mail. HOW a turn can be started from outside is the
// engine's own fact — typing into its TUI, posting to a socket it listens on —
// so each engine declares a WakeSpec (Engine.Wake), and the runner binds it
// ONCE per session into a Wake it fires with a nonce. Everything that does not
// depend on the engine — whether mail is pending, arming the nonce, the alarm
// when a wake goes unanswered — is the caller's, not the Wake's.

// Wake is one session's bound wake. Fire delivers spool.WakeText(nonce) as the
// prompt of a new turn, or refuses with a reason and delivers nothing.
type Wake interface {
	Fire(ctx context.Context, nonce string) error
}

// WakeSpec is an engine's declared way to wake a session. Bind fails — at bind
// time, naming the missing fact — when the session lacks what the spec needs;
// the runner never substitutes another spec at fire time.
type WakeSpec interface {
	Bind(ctx context.Context, s BoundSession) (Wake, error)
}

// BoundSession is what the runner holds for a live session that a spec may
// bind against. Each spec takes only what it needs.
type BoundSession struct {
	Harp string
	// Typed binds a wake that types into the session's pane, gated on
	// composer and on every input route the host knows of. nil when the
	// session is not pane-hosted.
	Typed func(composer ComposerProbe) (Wake, error)
}

// ComposerProbe reports whether an engine's input line is EMPTY, given the
// pane's cursor line up to the cursor. It is the engine's knowledge — what
// its prompt glyph and decorations look like — and the invariant it guards is
// that a typed wake never submits a human's draft.
type ComposerProbe func(line string) bool

// ErrWakeUnbound refuses a bind whose session lacks what the spec needs.
var ErrWakeUnbound = errors.New("engine: the session offers nothing this wake can bind to")

// TypedWakeSpec wakes by typing the wake text into the session's pane, once
// Composer proves the input line empty.
type TypedWakeSpec struct {
	Composer ComposerProbe
}

// Bind implements WakeSpec.
func (s TypedWakeSpec) Bind(_ context.Context, b BoundSession) (Wake, error) {
	if s.Composer == nil {
		return nil, errors.New("engine: a typed wake needs a composer probe; without one it could submit a draft")
	}
	if b.Typed == nil {
		return nil, fmt.Errorf("%w: a typed wake needs a pane, and session %s is not pane-hosted", ErrWakeUnbound, b.Harp)
	}
	return b.Typed(s.Composer)
}
