package sessions

// Locks is the session-liveness port: the ONE thing a reap decides liveness
// by. The adapter over the on-disk liveness lock implements it; this
// package never reads the lock itself.
type Locks interface {
	// Acquire probes harp's lock and, ONLY when its owner is provably dead,
	// keeps the lock until release is called — so the caller removes under
	// it, and a session resuming under the same harp meanwhile waits rather
	// than racing the deletion. On any other verdict Dead is false, Reason
	// says why, and release holds nothing. It never creates a lock file: a
	// file the probe minted would read as dead on the next probe.
	Acquire(harp string) (probe LockProbe, release func())
}

// LockProbe is what Locks.Acquire learned about one session's owner.
type LockProbe struct {
	// Dead is the ONLY verdict that permits removing: the lock file exists
	// and nothing holds it. A held lock, a missing one, an untrusted
	// filesystem and an unreadable lock all leave it false — "cannot
	// determine" is never permission.
	Dead bool
	// PID is read out of the lock file, for a human. It decides nothing.
	PID int
	// Reason is the human-readable why, whichever way Dead went.
	Reason string
}
