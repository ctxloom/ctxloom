package engine

import (
	"context"
	"errors"
)

// A WAKE starts a turn in an idle session so that session's turn-start hook
// runs and hands it its mail. HOW a turn can be started from outside is the
// engine's own fact — posting to a socket it listens on, for instance —
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
}

// ErrWakeUnbound refuses a bind whose session lacks what the spec needs.
var ErrWakeUnbound = errors.New("engine: the session offers nothing this wake can bind to")
