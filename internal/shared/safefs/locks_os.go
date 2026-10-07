package safefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

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

// lockOpenFlag is how every lock file is opened: created if missing,
// read-only (flock(2) needs no write access, and it is gofrs/flock's own
// default, so these locks contend with any gofrs/flock taker of the path),
// and hardened by lockOpenGuard against a planted FIFO. A symlink is
// followed: what the link resolves to is held to the regular-file check.
const lockOpenFlag = os.O_CREATE | os.O_RDONLY | lockOpenGuard

// tryLockRetry is how often TryLock re-attempts while it waits.
const tryLockRetry = 25 * time.Millisecond

// osLocks are kernel file locks (gofrs/flock: flock(2) on unix, LockFileEx
// on Windows). A lock dies with the process holding it, however it ends.
type osLocks struct{}

func (osLocks) Lock(path string) (Lock, error) {
	return takeOSLock(path, (*flock.Flock).Lock)
}

func (osLocks) RLock(path string) (Lock, error) {
	return takeOSLock(path, (*flock.Flock).RLock)
}

// takeOSLock is Lock and RLock: take blocks until the lock is held, in the
// kind it names.
func takeOSLock(path string, take func(*flock.Flock) error) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), lockDirMode); err != nil {
		return nil, fmt.Errorf("safefs: preparing lock directory for %s: %w", path, err)
	}
	// Checked before the acquire as well as after it: flock(2) on a
	// non-regular file can fail on its own terms first (darwin answers
	// ENOTSUP for a FIFO), which would surface as an acquisition error
	// rather than ErrNotRegularFile.
	if err := prepareLockFile(path); err != nil {
		return nil, err
	}
	fl := flock.New(path, flock.SetPermissions(lockFileMode), flock.SetFlag(lockOpenFlag))
	stop := lockwait.Watch(path)
	err := take(fl)
	stop()
	if err != nil {
		return nil, fmt.Errorf("safefs: acquiring lock %s: %w", path, err)
	}
	return heldOSLock(fl, path)
}

func (osLocks) TryLock(ctx context.Context, path string) (Lock, error) {
	if err := refuseNonRegular(path); err != nil {
		return nil, err
	}
	fl := flock.New(path, flock.SetPermissions(lockFileMode), flock.SetFlag(lockOpenFlag))
	got, err := fl.TryLock()
	if err == nil && !got && ctx.Err() == nil {
		got, err = fl.TryLockContext(ctx, tryLockRetry)
	}
	switch {
	case err != nil && ctx.Err() == nil:
		_ = fl.Close()
		return nil, fmt.Errorf("safefs: lock %s: %w", path, err)
	case !got:
		_ = fl.Close()
		return nil, fmt.Errorf("%w: %s", ErrLockHeld, path)
	}
	return heldOSLock(fl, path)
}

func (osLocks) Held(path string) (bool, error) {
	if err := refuseNonRegular(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	fl := flock.New(path, flock.SetFlag(os.O_RDONLY|lockOpenGuard))
	got, err := fl.TryLock()
	_ = fl.Close()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("safefs: probing lock %s: %w", path, err)
	}
	return !got, nil
}

// heldOSLock checks the handle actually locked — the path can be swapped
// between a check and the open — and wraps it.
func heldOSLock(fl *flock.Flock, path string) (Lock, error) {
	if err := requireRegular(fl, path); err != nil {
		_ = fl.Close()
		return nil, err
	}
	return osLock{fl: fl, path: path}, nil
}

type osLock struct {
	fl   *flock.Flock
	path string
}

func (l osLock) Current() bool {
	held, herr := l.fl.Stat()
	onDisk, derr := os.Stat(l.path)
	return herr == nil && derr == nil && os.SameFile(held, onDisk)
}

func (l osLock) Unlock() error { return l.fl.Close() }

// refuseNonRegular vets a lock path before it is opened: a path that exists
// as anything but a regular file is ErrNotRegularFile; a missing file is
// fine (a taker creates it) but a missing DIRECTORY is its stat error,
// fs.ErrNotExist, since nothing here creates one.
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
