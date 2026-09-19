// Package filelock is the toolbox's advisory file lock: one serialized
// read-modify-write transaction over a file some other process may also be
// rewriting, with the lock-wait watchdog on the acquire. It knows nothing of
// where a lock file belongs — the caller names the lock path — so a core
// leaf can decide the placement and any ring can take the lock.
package filelock

import (
	"fmt"
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
func WithLock(fs afero.Fs, lockPath string, fn func() error) error {
	if !IsOSBackedFs(fs) {
		return fn()
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		return fmt.Errorf("filelock: preparing lock directory for %s: %w", lockPath, err)
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	err := fl.Lock()
	stop()
	if err != nil {
		return fmt.Errorf("filelock: acquiring %s: %w", lockPath, err)
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
}
