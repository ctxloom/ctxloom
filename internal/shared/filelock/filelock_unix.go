//go:build !windows

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// lockFile acquires a lock on the file, blocking until available.
func lockFile(path string, shared bool) (func(), error) {
	if err := ensureDir(path); err != nil {
		return nil, errPrepare(path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	if err != nil {
		return nil, errOpen(path, err)
	}

	lockType := syscall.LOCK_EX
	if shared {
		lockType = syscall.LOCK_SH
	}

	if err := syscall.Flock(int(f.Fd()), lockType); err != nil {
		_ = f.Close()
		return nil, errAcquire(path, shared, err)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// tryLockFile acquires an EXCLUSIVE lock on path without blocking
// (LOCK_EX|LOCK_NB). acquired=false, err=nil means the lock is held
// elsewhere right now — flock(2) reports that as EWOULDBLOCK, which this
// distinguishes from every other Flock failure (a genuine environmental
// error, returned as err) precisely so a caller can tell "try later" from
// "something is actually broken".
func tryLockFile(path string) (unlock func(), acquired bool, err error) {
	if err := ensureDir(path); err != nil {
		return nil, false, errPrepare(path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	if err != nil {
		return nil, false, errOpen(path, err)
	}

	if ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); ferr != nil {
		_ = f.Close()
		if errors.Is(ferr, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, errAcquire(path, false, ferr)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
