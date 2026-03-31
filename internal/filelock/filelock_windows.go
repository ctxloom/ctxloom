//go:build windows

package filelock

import (
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
	lockfileExclusiveLock = 0x00000002
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

	var flags uint32
	if !shared {
		flags = lockfileExclusiveLock
	}
	// No LOCKFILE_FAIL_IMMEDIATELY = blocking

	if err := lockFileEx(syscall.Handle(f.Fd()), flags); err != nil {
		f.Close()
		return nil, err
	}

	return func() {
		unlockFileEx(syscall.Handle(f.Fd()))
		f.Close()
	}, nil
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
		0,                            // reserved, must be 0
		0xFFFFFFFF,                   // nNumberOfBytesToLockLow
		0xFFFFFFFF,                   // nNumberOfBytesToLockHigh
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
		0,                            // reserved, must be 0
		0xFFFFFFFF,                   // nNumberOfBytesToUnlockLow
		0xFFFFFFFF,                   // nNumberOfBytesToUnlockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)

	if r1 == 0 {
		return err
	}
	return nil
}
