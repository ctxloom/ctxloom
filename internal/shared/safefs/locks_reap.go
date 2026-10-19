package safefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
)

// lockFileRemover is a held lock that can unlink its own lock file, on the
// filesystem its Locks live on.
type lockFileRemover interface {
	removeLockFile() error
}

// ReapLockFile removes the lock file at path if, and only if, no taker holds
// it, and reports whether it did. A lock file that does not exist is not
// reaped and is no error; one some taker holds is not reaped either.
//
// It opens the file without creating it, takes it with ONE non-blocking
// attempt, confirms what it took is still the file at path (Lock.Current),
// and unlinks it while still holding it, releasing it after. Unlinking under
// the lock is what makes removal safe against the takers of this package: a
// taker that opened the file before the unlink is granted it only after,
// finds it no longer at the path, and takes the lock again on whatever file
// is there (Locks). A taker that does NOT re-check — a binary older than
// LockProtocol — would keep the unlinked file and hold the lock beside the
// next taker, so a caller must first establish that none can be running.
func ReapLockFile(l Locks, path string) (reaped bool, err error) {
	once, cancel := context.WithCancel(context.Background())
	cancel() // a done context: one attempt, never a wait
	lk, err := l.TryLockExisting(once, path)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrLockHeld):
		return false, nil
	case err != nil:
		return false, err
	}
	defer func() { _ = lk.Unlock() }()
	if !lk.Current() {
		return false, nil
	}
	rm, ok := lk.(lockFileRemover)
	if !ok {
		return false, fmt.Errorf("safefs: reap %s: a %T cannot remove the lock file it holds", path, lk)
	}
	if err := rm.removeLockFile(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("safefs: reap %s: %w", path, err)
	}
	return true, nil
}
