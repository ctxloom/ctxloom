//go:build !windows

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// ErrLocked is returned when TryLock fails because the file is already locked.
var ErrLocked = errors.New("file is locked")

// lockFile acquires a lock on the file, blocking until available.
func lockFile(path string, shared bool) (func(), error) {
	if err := ensureDir(path); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}

	lockType := syscall.LOCK_EX
	if shared {
		lockType = syscall.LOCK_SH
	}

	if err := syscall.Flock(int(f.Fd()), lockType); err != nil {
		f.Close()
		return nil, err
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// tryLockFile attempts to acquire a lock without blocking.
func tryLockFile(path string, shared bool) (func(), error) {
	if err := ensureDir(path); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}

	lockType := syscall.LOCK_EX | syscall.LOCK_NB
	if shared {
		lockType = syscall.LOCK_SH | syscall.LOCK_NB
	}

	if err := syscall.Flock(int(f.Fd()), lockType); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
