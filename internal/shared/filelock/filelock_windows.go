//go:build windows

package filelock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	// Windows lock flags
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// errorLockViolation is Windows ERROR_LOCK_VIOLATION (33): the specific
// failure LockFileEx reports for "already locked by someone else" when
// LOCKFILE_FAIL_IMMEDIATELY is set, as distinct from any other reason the
// call could fail.
const errorLockViolation = syscall.Errno(33)

// lockFile acquires a lock on the file, blocking until available.
func lockFile(path string, shared bool) (func(), error) {
	if err := ensureDir(path); err != nil {
		return nil, errPrepare(path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	if err != nil {
		return nil, errOpen(path, err)
	}

	var flags uint32
	if !shared {
		flags = lockfileExclusiveLock
	}
	// No LOCKFILE_FAIL_IMMEDIATELY = blocking

	if err := lockFileEx(syscall.Handle(f.Fd()), flags); err != nil {
		f.Close()
		return nil, errAcquire(path, shared, err)
	}

	return func() {
		unlockFileEx(syscall.Handle(f.Fd()))
		f.Close()
	}, nil
}

// tryLockFile acquires an EXCLUSIVE lock on path without blocking
// (LOCKFILE_FAIL_IMMEDIATELY). acquired=false, err=nil means the lock is
// held elsewhere right now (ERROR_LOCK_VIOLATION); any other LockFileEx
// failure is a genuine environmental error, returned as err.
func tryLockFile(path string) (unlock func(), acquired bool, err error) {
	if err := ensureDir(path); err != nil {
		return nil, false, errPrepare(path, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	if err != nil {
		return nil, false, errOpen(path, err)
	}

	if lerr := lockFileEx(syscall.Handle(f.Fd()), lockfileExclusiveLock|lockfileFailImmediately); lerr != nil {
		f.Close()
		if errors.Is(lerr, errorLockViolation) {
			return nil, false, nil
		}
		return nil, false, errAcquire(path, false, lerr)
	}

	return func() {
		unlockFileEx(syscall.Handle(f.Fd()))
		f.Close()
	}, true, nil
}

// lockFileEx wraps the Windows LockFileEx API.
// Locks the entire file (offset 0, length max).
func lockFileEx(handle syscall.Handle, flags uint32) error {
	// OVERLAPPED structure for async I/O (we use synchronous, so it's zeroed)
	var overlapped syscall.Overlapped

	// Lock entire file: offset 0, length 0xFFFFFFFF (max)
	r1, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		0,          // reserved, must be 0
		0xFFFFFFFF, // nNumberOfBytesToLockLow
		0xFFFFFFFF, // nNumberOfBytesToLockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if r1 == 0 {
		return err
	}
	return nil
}

// unlockFileEx releases the lock on the file.
func unlockFileEx(handle syscall.Handle) error {
	var overlapped syscall.Overlapped

	r1, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		0,          // reserved, must be 0
		0xFFFFFFFF, // nNumberOfBytesToUnlockLow
		0xFFFFFFFF, // nNumberOfBytesToUnlockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if r1 == 0 {
		return err
	}
	return nil
}
