//go:build windows

package safefs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh places the one locked byte at 0x7fffffff_00000000, far past
// any content. Windows byte-range locks are MANDATORY: a lock over byte 0
// makes that byte unreadable and unwritable through every other handle — a
// session lock's pid could never be read while its owner lives, and could
// not be stamped in place while a sweeper holds the lock. Locking past EOF is
// legal and conflicts only with the same range, so every taker of a lock file
// must come through these locks.
const lockOffsetHigh = 0x7fffffff

// winLock is one handle and the locks taken through it. The handle is opened
// with FILE_SHARE_DELETE — the POSIX behaviour, where an open file can still
// be unlinked — because a lock file may be removed while it is held (a
// session Hold that loses removes it under the winner). Closing the handle
// releases its locks.
type winLock struct {
	path        string
	disposition uint32
	f           *os.File
}

func newKernelLock(path string, create bool) kernelLock {
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.OPEN_ALWAYS
	}
	return &winLock{path: path, disposition: disposition}
}

func (l *winLock) open() error {
	if l.f != nil {
		return nil
	}
	name, err := windows.UTF16PtrFromString(l.path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, l.disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: l.path, Err: err}
	}
	l.f = os.NewFile(uintptr(h), l.path)
	return nil
}

// lock takes the past-EOF byte with flags: blocking unless
// LOCKFILE_FAIL_IMMEDIATELY, shared unless LOCKFILE_EXCLUSIVE_LOCK.
func (l *winLock) lock(flags uint32) error {
	if err := l.open(); err != nil {
		return err
	}
	ol := windows.Overlapped{OffsetHigh: lockOffsetHigh}
	return windows.LockFileEx(windows.Handle(l.f.Fd()), flags, 0, 1, 0, &ol)
}

func (l *winLock) Lock() error  { return l.lock(windows.LOCKFILE_EXCLUSIVE_LOCK) }
func (l *winLock) RLock() error { return l.lock(0) }

func (l *winLock) TryLock() (bool, error) {
	err := l.lock(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

// TryLockContext retries TryLock every retry until it succeeds, errors, or
// ctx ends — gofrs/flock's contract, which osLocks is written against.
func (l *winLock) TryLockContext(ctx context.Context, retry time.Duration) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if ok, err := l.TryLock(); ok || err != nil {
			return ok, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(retry):
		}
	}
}

func (l *winLock) Stat() (fs.FileInfo, error) {
	if err := l.open(); err != nil {
		return nil, err
	}
	return l.f.Stat()
}

func (l *winLock) Close() error {
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
