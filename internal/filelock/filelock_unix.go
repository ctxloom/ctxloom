//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

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
		_ = f.Close()
		return nil, err
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
