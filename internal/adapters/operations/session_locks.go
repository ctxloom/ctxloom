package operations

import (
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// sessionLocks is sessions.Locks over the on-disk liveness lock
// (sessionlock): the one adapter every reap decides liveness through.
type sessionLocks struct{}

// SessionLocks is the session-liveness port over the on-disk lock, for a
// composition outside this package (the runner's sweep).
func SessionLocks() sessions.Locks { return sessionLocks{} }

// Acquire is sessionlock.Acquire projected onto the port: Dead is the lock's
// one permitting verdict (sessionlock.Verdict.MayReclaim); held, missing,
// untrusted and unreadable all arrive as not dead with the lock's own
// reason.
func (sessionLocks) Acquire(harp string) (sessions.LockProbe, func()) {
	probe, release := sessionlock.Acquire(harp)
	return sessions.LockProbe{Dead: probe.Verdict.MayReclaim(), PID: probe.PID, Reason: probe.Reason}, release
}

// lockProbe rebuilds the lock's own probe from the port's, for the worktree
// classifier that takes it. Exact, because the triage runs only once the
// reaper has seen Dead.
func lockProbe(p sessions.LockProbe) sessionlock.Probe {
	verdict := sessionlock.Indeterminate
	if p.Dead {
		verdict = sessionlock.Dead
	}
	return sessionlock.Probe{Verdict: verdict, PID: p.PID, Reason: p.Reason}
}

// LockSessionState is the production SessionStateProbe: the session lock's
// verdict, read without holding anything (sessionlock.Inspect). It comes from
// the lock and from nothing else — never from the record's ended_at, which
// is a fact about the past that lies about now in both directions: a crashed
// session never wrote one and would read as live forever, and a session
// resumed under its harp still carries the old one and would read as ended
// while it runs. A harp this process itself holds is answered from memory
// (sessionlock.Acquire), so an in-process caller cannot drop its own lock.
func LockSessionState(harp string) string {
	switch sessionlock.Inspect(harp).Verdict {
	case sessionlock.Alive:
		return SessionLive
	case sessionlock.Dead:
		return SessionEnded
	default:
		return SessionUnknown
	}
}
