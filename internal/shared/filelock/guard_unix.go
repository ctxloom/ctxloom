//go:build unix

package filelock

import "syscall"

// openGuard hardens the lock-file open against whatever another principal
// planted at the lock path. O_NOFOLLOW: a symlink is refused (ELOOP) instead
// of followed to create or lock a file of the planter's choosing. O_NONBLOCK:
// a read-only open of a FIFO otherwise blocks until a writer appears — a
// planted FIFO would hang the host forever before any type check could run.
// flock(2) ignores the descriptor's O_NONBLOCK, so lock waiting is unchanged.
const openGuard = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
