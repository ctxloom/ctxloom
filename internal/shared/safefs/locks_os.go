package safefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// lockFileMode is the mode a lock file is created with, before umask. It is
// deliberately not group- or world-WRITABLE: acquiring a lock opens the file,
// so its write bits are exactly the list of accounts that can take it, and
// through that, block every other account's writes to the resource it
// protects. Widening this is a decision about who may block whom, not a
// formatting one.
const lockFileMode = 0o644

// lockDirMode is the mode Lock creates a lock file's parent directory with,
// before umask: traversable, and not group- or world-writable.
const lockDirMode = 0o755

// lockOpenFlag is how every lock file is opened for taking: created if
// missing, read-only (flock(2) needs no write access), and hardened by
// lockOpenGuard against a planted FIFO. A symlink is followed: what the link
// resolves to is held to the regular-file check.
const lockOpenFlag = os.O_CREATE | os.O_RDONLY | lockOpenGuard

// tryLockRetry is how often TryLock re-attempts while it waits.
const tryLockRetry = 25 * time.Millisecond

// osLocks are kernel file locks (newKernelLock: flock(2) on unix, LockFileEx
// over a byte past the content on Windows). A lock dies with the process
// holding it, however it ends.
type osLocks struct{}

// kernelLock is one handle's kernel lock on one path, as newKernelLock
// builds it per platform. Close releases whatever it holds.
type kernelLock interface {
	Lock() error
	RLock() error
	TryLock() (bool, error)
	TryLockContext(ctx context.Context, retry time.Duration) (bool, error)
	TryRLock() (bool, error)
	TryRLockContext(ctx context.Context, retry time.Duration) (bool, error)
	Stat() (fs.FileInfo, error)
	Close() error
}

func (osLocks) Lock(path string) (Lock, error) {
	return takeOSLock(path, kernelLock.Lock, "")
}

func (osLocks) RLock(path string) (Lock, error) {
	return takeOSLock(path, kernelLock.RLock, "")
}

// LockGuarding is Lock, recording guarded in a lock file it creates (see
// GuardingLocks).
func (osLocks) LockGuarding(path, guarded string) (Lock, error) {
	return takeOSLock(path, kernelLock.Lock, guarded)
}

// takeOSLock is Lock, RLock and LockGuarding: take blocks until the lock is
// held, in the kind it names, and is retried while what it was granted is no
// longer the file at path (takeCurrent). A non-empty guarded is recorded in
// a lock file an attempt creates — every attempt, since the file a retry
// creates is a new one.
func takeOSLock(path string, take func(kernelLock) error, guarded string) (Lock, error) {
	return takeCurrent(path, func() (Lock, error) { return takeOSLockOnce(path, take, guarded) })
}

// takeOSLockOnce is one attempt of takeOSLock, granted on whichever file the
// path named when it was opened.
func takeOSLockOnce(path string, take func(kernelLock) error, guarded string) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), lockDirMode); err != nil {
		return nil, fmt.Errorf("safefs: preparing lock directory for %s: %w", path, err)
	}
	if guarded != "" {
		if err := recordGuarded(path, guarded); err != nil {
			return nil, err
		}
	}
	// Checked before the acquire as well as after it: flock(2) on a
	// non-regular file can fail on its own terms first (darwin answers
	// ENOTSUP for a FIFO), which would surface as an acquisition error
	// rather than ErrNotRegularFile.
	if err := prepareLockFile(path); err != nil {
		return nil, err
	}
	fl := newKernelLock(path, true)
	stop := lockwait.Watch(path)
	err := take(fl)
	stop()
	if err != nil {
		return nil, fmt.Errorf("safefs: acquiring lock %s: %w", path, err)
	}
	return heldOSLock(fl, path)
}

func (osLocks) TryLock(ctx context.Context, path string) (Lock, error) {
	return tryOSLock(ctx, path, true, false)
}

func (osLocks) TryLockExisting(ctx context.Context, path string) (Lock, error) {
	return tryOSLock(ctx, path, false, false)
}

func (osLocks) TryRLock(ctx context.Context, path string) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), lockDirMode); err != nil {
		return nil, fmt.Errorf("safefs: preparing lock directory for %s: %w", path, err)
	}
	return tryOSLock(ctx, path, true, true)
}

// tryOSLock is TryLock, TryLockExisting and TryRLock; create says whether a
// missing lock file is created, shared whether the lock is taken shared.
// Only the wait running out is ErrLockHeld: a failure to open or lock is
// returned as itself, however done ctx is. It is retried while what it was
// granted is no longer the file at path (takeCurrent): a retry with a done
// ctx still makes its one attempt.
func tryOSLock(ctx context.Context, path string, create, shared bool) (Lock, error) {
	return takeCurrent(path, func() (Lock, error) { return tryOSLockOnce(ctx, path, create, shared) })
}

// tryOSLockOnce is one attempt of tryOSLock. A wait re-opens the path on
// each poll, so what it is granted is the file the last poll opened.
func tryOSLockOnce(ctx context.Context, path string, create, shared bool) (Lock, error) {
	if err := refuseNonRegular(path); err != nil {
		return nil, err
	}
	fl := newKernelLock(path, create)
	try, wait := fl.TryLock, fl.TryLockContext
	if shared {
		try, wait = fl.TryRLock, fl.TryRLockContext
	}
	got, err := try()
	if err == nil && !got && ctx.Err() == nil {
		got, err = wait(ctx, tryLockRetry)
		if err != nil && errors.Is(err, ctx.Err()) {
			err = nil
		}
	}
	switch {
	case err != nil:
		_ = fl.Close()
		return nil, fmt.Errorf("safefs: lock %s: %w", path, err)
	case !got:
		_ = fl.Close()
		return nil, fmt.Errorf("%w: %s", ErrLockHeld, path)
	}
	return heldOSLock(fl, path)
}

// Held probes as a taker takes: a probe granted the lock on a file no longer
// at the path has answered about a file nobody else can open, while the one
// at the path may be held, so it probes again.
func (osLocks) Held(path string) (bool, error) {
	for range lockAttempts {
		held, current, err := probeOSLock(path)
		if err != nil || current {
			return held, err
		}
	}
	return false, fmt.Errorf("%w: %s, %d times", ErrLockReplaced, path, lockAttempts)
}

// probeOSLock is one attempt of Held. current is false only for a probe
// granted the lock on a file no longer at the path; a held lock is reported
// as held whichever file it is on.
func probeOSLock(path string) (held, current bool, err error) {
	if err := refuseNonRegular(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, true, nil
		}
		return false, true, err
	}
	fl := newKernelLock(path, false)
	defer func() { _ = fl.Close() }()
	got, err := fl.TryLock()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, true, nil
	case err != nil:
		return false, true, fmt.Errorf("safefs: probing lock %s: %w", path, err)
	case !got:
		return true, true, nil
	}
	return false, sameFileAt(fl, path), nil
}

// heldOSLock checks the handle actually locked — the path can be swapped
// between a check and the open — and wraps it.
func heldOSLock(fl kernelLock, path string) (Lock, error) {
	if err := requireRegular(fl, path); err != nil {
		_ = fl.Close()
		return nil, err
	}
	return osLock{fl: fl, path: path}, nil
}

type osLock struct {
	fl   kernelLock
	path string
}

func (l osLock) Current() bool { return sameFileAt(l.fl, l.path) }

func (l osLock) Unlock() error { return l.fl.Close() }

// removeLockFile unlinks the lock file at the lock's path (ReapLockFile).
func (l osLock) removeLockFile() error { return os.Remove(l.path) }

// sameFileAt reports whether the file fl has open is the one at path now.
func sameFileAt(fl kernelLock, path string) bool {
	held, herr := fl.Stat()
	onDisk, derr := os.Stat(path)
	return herr == nil && derr == nil && os.SameFile(held, onDisk)
}

// refuseNonRegular vets a lock path before it is opened: a path that exists
// as anything but a regular file is ErrNotRegularFile; a missing file is
// left to the open (TryLock creates it, TryLockExisting fails on it) but a
// missing DIRECTORY is its stat error, fs.ErrNotExist, since nothing here
// creates one.
func refuseNonRegular(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, perr := os.Stat(filepath.Dir(path)); perr != nil {
			return perr
		}
		return nil
	case err != nil:
		return fmt.Errorf("safefs: lock %s: %w", path, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%w: %s is %s", ErrNotRegularFile, path, info.Mode().Type())
	}
	return nil
}

// recordGuarded is createRecording on the real filesystem, hardened against
// a planted FIFO as every lock-file open is (lockOpenGuard).
func recordGuarded(path, guarded string) error {
	return createRecording(path, guarded, func(flag int) (io.WriteCloser, error) {
		return os.OpenFile(path, flag|lockOpenGuard, lockFileMode)
	})
}

// prepareLockFile creates the lock file at path without taking the lock,
// refusing a path that resolves to anything but a regular file.
func prepareLockFile(path string) error {
	f, err := os.OpenFile(path, lockOpenFlag, lockFileMode)
	if err != nil {
		return fmt.Errorf("safefs: opening lock %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return requireRegular(f, path)
}

// requireRegular stats the OPEN handle — never the path, which could be
// swapped between a path check and the open — and refuses anything but a
// regular file.
func requireRegular(f interface{ Stat() (fs.FileInfo, error) }, path string) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("safefs: stat lock %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is %s", ErrNotRegularFile, path, info.Mode().Type())
	}
	return nil
}
