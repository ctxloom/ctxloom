//go:build unix

package safefs

import "syscall"

// lockOpenGuard hardens the lock-file open against a FIFO at the lock path: a
// read-only open of one otherwise blocks until a writer appears, hanging the
// caller before any type check could run. O_NONBLOCK makes that open return
// so requireRegular can refuse it. flock(2) ignores the descriptor's
// O_NONBLOCK, so lock waiting is unchanged.
const lockOpenGuard = syscall.O_NONBLOCK
