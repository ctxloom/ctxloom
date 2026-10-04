// Package filelock is the toolbox's advisory file lock: one serialized
// read-modify-write transaction over a file some other process may also be
// rewriting, with the lock-wait watchdog on the acquire. It knows nothing of
// where a lock file belongs — the caller names the lock path — so a core
// leaf can decide the placement and any ring can take the lock.
package filelock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// lockFileMode is the mode a lock file is created with, before umask. It is
// deliberately not group- or world-WRITABLE: acquiring a lock opens the file
// for writing, so its write bits are exactly the list of accounts that can
// take it, and through that, block every other account's writes to the
// resource it protects. Widening this is a decision about who may block
// whom, not a formatting one.
const lockFileMode = 0o644

// lockDirMode is the mode the lock file's parent directory is created with,
// before umask — the execute bit a directory needs to be traversable at all,
// paired with the same not-group-or-world-writable stance as lockFileMode.
const lockDirMode = 0o755

// openFlag is how every lock file is opened: created if missing, read-only
// (flock(2) needs no write access, and it is gofrs/flock's own default), and
// hardened by openGuard against a planted FIFO. A symlink is followed: a user
// or agent may link a lock file wherever they choose, and what the link
// resolves to is held to the same regular-file check.
const openFlag = os.O_CREATE | os.O_RDONLY | openGuard

// ErrNotRegularFile is the refusal of a lock path that resolves to anything
// but a regular file — a FIFO, a device, a directory. Locking one is not
// locking at all: a FIFO blocks or cannot be flocked, and a device is shared
// with everything else that opens it.
var ErrNotRegularFile = errors.New("filelock: lock path is not a regular file")

// IsOSBackedFs reports whether fs is the real operating-system filesystem, as
// opposed to a test double (afero.MemMapFs, a ReadOnlyFs wrapping one, ...).
// A nil fs is the OS filesystem by the family's convention.
func IsOSBackedFs(fs afero.Fs) bool {
	if fs == nil {
		return true
	}
	_, ok := fs.(*afero.OsFs)
	return ok
}

// WithLock runs fn as ONE serialized transaction under the advisory lock at
// lockPath. fn is the WHOLE cycle — read, modify, write — never just the
// write: the lock has to be held before the read for "fresh" to mean
// anything.
//
// Locking is skipped entirely when fs, the caller's own filesystem seam, is
// not OS-backed: locking exists to exclude OTHER PROCESSES, which a test
// double has none of, and asking the REAL OS to create and flock a path a
// test never intended would touch actual disk.
//
// A lock ACQUISITION failure fails the whole call closed: flock.Flock.Lock
// only errors on a persistent environmental failure (never ordinary
// contention, which it already waits out), so proceeding unlocked on that
// failure would silently discard the one guarantee this function exists to
// provide. fn never runs on that path.
//
// The lock file is checked twice. Before the acquire (Prepare), because
// flock(2) on a non-regular file can fail on its own terms first — darwin
// answers ENOTSUP for a FIFO — which would surface as an acquisition error
// rather than ErrNotRegularFile. After it, on the handle actually locked,
// because the path can be swapped between the two opens.
func WithLock(fs afero.Fs, lockPath string, fn func() error) error {
	if !IsOSBackedFs(fs) {
		return fn()
	}
	if err := Prepare(lockPath); err != nil {
		return err
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode), flock.SetFlag(openFlag))
	stop := lockwait.Watch(lockPath)
	err := fl.Lock()
	stop()
	if err != nil {
		return fmt.Errorf("filelock: acquiring %s: %w", lockPath, err)
	}
	defer func() { _ = fl.Unlock() }()
	if err := requireRegular(fl, lockPath); err != nil {
		return err
	}
	return fn()
}

// Prepare creates the lock file at lockPath (and its directory) without
// taking the lock, under the same refusals WithLock applies — for a caller
// that must hand the file to someone else, such as a bind-mount source that
// has to exist as a FILE before the runtime sees it.
func Prepare(lockPath string) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		return fmt.Errorf("filelock: preparing lock directory for %s: %w", lockPath, err)
	}
	f, err := os.OpenFile(lockPath, openFlag, lockFileMode)
	if err != nil {
		return fmt.Errorf("filelock: acquiring %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	return requireRegular(f, lockPath)
}

// requireRegular stats the OPEN handle — never the path, which could be
// swapped between a path check and the open — and refuses anything but a
// regular file.
func requireRegular(f interface{ Stat() (fs.FileInfo, error) }, lockPath string) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("filelock: stat %s: %w", lockPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is %s", ErrNotRegularFile, lockPath, info.Mode().Type())
	}
	return nil
}
