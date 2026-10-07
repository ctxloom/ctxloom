package safefs

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/spf13/afero"
)

// The modes an owner-only directory and file are created with. On unix they
// ARE the protection; on Windows they are what os honours of them (the
// read-only attribute) and the protection is the directory's DACL, which a
// file created inside it inherits.
const (
	PrivateDirMode  fs.FileMode = 0o700
	PrivateFileMode fs.FileMode = 0o600
)

// Root is ctxloom's root as the controller sees it: the filesystem it runs
// on, paired with what acts on those same files — owner-only protection and
// advisory locks. They come as ONE value so nothing below ever asks what kind
// of filesystem it was handed: a component given a Root locks and protects
// through it, and a test's in-memory Root carries in-memory locks and mode
// bits rather than reaching the real disk.
type Root struct {
	Fs      afero.Fs
	Private Private
	Locks   Locks
}

// Private makes directories private to the user ctxloom runs as, and checks
// that paths still are. What "owner-only" means is the platform's business:
// on unix the mode bits (no group or other bit); on Windows the DACL, with
// SYSTEM and the Administrators group tolerated beside the owner (ruled
// 2026-09-25: both can take any file on the machine regardless).
type Private interface {
	// Ensure creates dir owner-only if it is missing; an existing dir it
	// restricts only when Check finds it exposed — on Windows a restriction
	// propagates to every inheriting child, so restricting an
	// already-private tree on every start would walk all of it. What is
	// created inside dir afterwards is owner-only too (unix: a 0700 parent
	// blocks traversal; Windows: the restricted DACL is inheritable).
	Ensure(dir string) error
	// Check holds each path to owner-only, in order, and returns the first
	// that is not as an *ExposedError. A path that cannot be stat'ed is
	// returned as the stat error (so a missing one is fs.ErrNotExist).
	Check(paths ...string) error
}

// ExposedError is a path open to someone beyond its owner (and, on Windows,
// the tolerated machine principals). Why says how, in the platform's terms: a
// mode on unix, the principals granted access on Windows.
type ExposedError struct {
	Path string
	Why  string
}

func (e *ExposedError) Error() string { return e.Path + " " + e.Why }

// Locks are advisory locks on files of the Root's filesystem: one exclusive
// holder per path at a time, or any number of shared ones, across processes
// on the real filesystem and across goroutines everywhere (a lock conflicts
// per taking, not per process).
type Locks interface {
	// Lock blocks until path's lock is held exclusively, creating the lock
	// file and its directory if missing. A wait that runs long is reported
	// (lockwait).
	Lock(path string) (Lock, error)
	// RLock blocks until path's lock is held SHARED: beside any other shared
	// holder, never beside an exclusive one. It creates the lock file and its
	// directory as Lock does, and a long wait is reported the same way.
	RLock(path string) (Lock, error)
	// TryLock makes one attempt at once, then retries until ctx is done, and
	// is ErrLockHeld if the lock is still held then. It creates a missing
	// lock file but NEVER its directory: a directory removed under a
	// claimant is fs.ErrNotExist, which is how the claimant learns of it.
	TryLock(ctx context.Context, path string) (Lock, error)
	// TryLockExisting is TryLock on a lock file that must already exist: it
	// creates nothing, and a missing file is fs.ErrNotExist (however done
	// ctx is), never ErrLockHeld. A prober that must not leave behind a lock
	// file it created takes this.
	TryLockExisting(ctx context.Context, path string) (Lock, error)
	// Held probes whether some taker holds path's lock, shared or exclusive,
	// without taking it for longer than the probe and without creating
	// anything. A lock file that does not exist is not held.
	Held(path string) (bool, error)
}

// Lock is one held lock.
type Lock interface {
	// Current reports the locked file is still the one at its path. A
	// removal made under the lock unlinks the file; a claimant that opened
	// it before and was granted it after holds a lock nobody else can see.
	Current() bool
	// Unlock releases the lock.
	Unlock() error
}

// ErrLockHeld is TryLock's "another taker still holds it".
var ErrLockHeld = errors.New("safefs: the lock is held")

// ErrNotRegularFile is the refusal of a lock path that resolves to anything
// but a regular file — a FIFO, a device, a directory. Locking one is not
// locking at all: a FIFO blocks or cannot be locked, and a device is shared
// with everything else that opens it.
var ErrNotRegularFile = errors.New("safefs: lock path is not a regular file")

// WithLock runs fn as ONE serialized transaction under path's lock. fn is the
// WHOLE cycle — read, modify, write — never just the write: the lock has to
// be held before the read for "fresh" to mean anything. It never skips the
// lock: a failure to take it fails the call, and fn does not run.
func WithLock(l Locks, path string, fn func() error) error {
	lk, err := l.Lock(path)
	if err != nil {
		return err
	}
	defer func() { _ = lk.Unlock() }()
	return fn()
}

// New is the controller's own filesystem: real files, the platform's
// owner-only protection, and kernel file locks. Wherever the controller runs
// — the machine's disk or a container's — this is ctxloom's root there.
func New() Root {
	return Root{Fs: afero.NewOsFs(), Private: osPrivate(), Locks: osLocks{}}
}

// NewMem is a Root over fsys for tests: owner-only is fsys's mode bits, and
// the locks are in-process ones that really serialize. Every Root NewMem
// builds over the same fsys shares its locks, as processes on one disk do.
func NewMem(fsys afero.Fs) Root {
	// restrict cleans dir because a MemMapFs files every entry under its
	// cleaned path but looks Chmod's name up exactly as given: an uncleaned
	// name — any slash path on Windows — names no entry.
	restrict := func(dir string) error { return fsys.Chmod(filepath.Clean(dir), PrivateDirMode) }
	return Root{
		Fs:      fsys,
		Private: privateOn{fs: fsys, violation: modeViolation, restrict: restrict},
		Locks:   memLocks{fs: fsys},
	}
}
