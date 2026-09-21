package operations

import (
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// sessionLocks is sessions.Locks over the on-disk liveness lock
// (sessionlock): the one adapter every reap decides liveness through.
type sessionLocks struct{}

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
