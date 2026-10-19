package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// ErrLockReapInProgress is the refusal of an activity that takes or binds a
// home lock sidecar while a reap of the home lock directory (`ctxloom clean
// --yes`) holds the reap interlock, past ReapInterlockWait.
var ErrLockReapInProgress = errors.New("a reap of the home lock directory (`ctxloom clean --yes`) is in progress")

// ReapInterlockWait bounds how long such an activity waits for a reap to
// finish before it is refused. A reap holds the interlock for its gate's
// checks and one pass over the directory, so this is generous next to it.
// A var so a test can shorten it.
var ReapInterlockWait = 30 * time.Second

// HoldReapInterlock takes the reap interlock (paths.LockReapInterlockPath)
// SHARED for one activity that takes or binds a sidecar of the home lock
// directory, and returns its release. Every such activity holds it for as
// long as it may take or bind one; the reap holds it exclusive for as long as
// it judges and removes them, so the two never overlap. A reap in progress is
// waited out for at most ReapInterlockWait, then refused
// (ErrLockReapInProgress): the activity never proceeds without its hold.
//
// The interlock adds nothing to a lone taker's safety — every taker already
// re-checks its lock file and retries on a removed one (safefs.Locks). It is
// what a container bind needs, whose single-file bind keeps the file it
// bound whatever the host does to the path, and it keeps a reap from running
// beside any taker at all once the reap's gate has passed.
func HoldReapInterlock(locks safefs.Locks) (release func(), err error) {
	path, err := paths.LockReapInterlockPath()
	if err != nil {
		return nil, fmt.Errorf("sessions: the reap interlock: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ReapInterlockWait)
	defer cancel()
	lk, err := locks.TryRLock(ctx, path)
	switch {
	case errors.Is(err, safefs.ErrLockHeld):
		return nil, fmt.Errorf("%w: it still held %s after %s; let it finish, then retry", ErrLockReapInProgress, path, ReapInterlockWait)
	case err != nil:
		return nil, fmt.Errorf("sessions: the reap interlock: %w", err)
	}
	return func() { _ = lk.Unlock() }, nil
}

// holdReapInterlockFor holds the reap interlock around taking lockPath when
// lockPath is a sidecar the reap can remove — one directly in the home lock
// directory — and holds nothing for any other lock: a session's own sidecars
// are reaped with the session, under its liveness lock, never by this reap.
func holdReapInterlockFor(locks safefs.Locks, lockPath string) (release func(), err error) {
	dir, err := paths.HomeLocksDir()
	if err != nil || filepath.Dir(lockPath) != dir {
		return func() {}, nil
	}
	return HoldReapInterlock(locks)
}
