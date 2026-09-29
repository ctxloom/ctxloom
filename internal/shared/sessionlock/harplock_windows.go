//go:build windows

package sessionlock

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// harpLockOffsetHigh places the locked byte at 0x7fffffff_00000000, far past
// any pid, instead of gofrs/flock's byte 0. Windows byte-range locks are
// MANDATORY: a lock over byte 0 makes the pid unreadable through every other
// handle (a human, and this package's own readPID, could never see who holds
// it) and refuses Hold's in-place stamp while a sweeper holds the lock.
// Locking past EOF is legal and conflicts only with the same range, so every
// holder and probe of a harp lock must come through newHarpLock.
const harpLockOffsetHigh = 0x7fffffff

const (
	harpLockFlagCreate   = windows.OPEN_ALWAYS
	harpLockFlagExisting = windows.OPEN_EXISTING
)

// winHarpLock is one handle and, once TryLock succeeds, its lock. The handle
// is opened with FILE_SHARE_DELETE — the POSIX behaviour where an open file
// can still be unlinked — because a Hold that loses must remove the file
// while the winner still has it open (see Hold).
type winHarpLock struct {
	path        string
	disposition uint32
	mu          sync.Mutex
	f           *os.File
}

func newHarpLock(path string, disposition int) harpLock {
	return &winHarpLock{path: path, disposition: uint32(disposition)} //nolint:gosec // one of the two creation dispositions above
}

func (l *winHarpLock) open() (*os.File, error) {
	name, err := windows.UTF16PtrFromString(l.path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, l.disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: l.path, Err: err}
	}
	return os.NewFile(uintptr(h), l.path), nil
}

// TryLock takes the lock without waiting. A lost race closes the handle, as
// gofrs/flock does, so an unlocked lock holds nothing open.
func (l *winHarpLock) TryLock() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		return true, nil
	}
	f, err := l.open()
	if err != nil {
		return false, err
	}
	ol := windows.Overlapped{OffsetHigh: harpLockOffsetHigh}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	switch {
	case err == nil:
		l.f = f
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		_ = f.Close()
		return false, nil
	default:
		_ = f.Close()
		return false, err
	}
}

// TryLockContext polls TryLock every retry until it succeeds, errors, or ctx
// ends — gofrs/flock's contract, which Hold is written against.
func (l *winHarpLock) TryLockContext(ctx context.Context, retry time.Duration) (bool, error) {
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

// Close releases the lock by closing its handle; the kernel drops a
// handle's byte-range locks with it.
func (l *winHarpLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
