package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ownedScratchLockName is the lock file inside every owned scratch dir. The
// owner holds an exclusive lock on it (lockOwnerFile) for the scratch's whole
// life, and the kernel drops that lock when the owner exits by ANY route — a
// SIGKILL or an OOM included, where no deferred cleanup runs. So "the lock is
// obtainable" is exactly "the owner is gone", which is the liveness answer a
// reaper needs: an age cutoff can only guess at it, and a PID check is wrong
// across PID namespaces sharing one filesystem. The lock conflicts per open
// handle, so the answer is also right between goroutines of one process.
const ownedScratchLockName = ".ctxloom-owner.lock"

// ownedScratchLockMode keeps the lock file owner-only: a lock is taken on an
// open handle, so its readers are exactly who can hold the scratch live.
const ownedScratchLockMode = 0o600

// ownedScratchAttempts bounds creation retries. A retry happens only when a
// concurrent reaper took the freshly made dir in the instant between its
// creation and its lock — see newOwnedScratch.
const ownedScratchAttempts = 3

// scratchCreated runs between a scratch dir's creation and its lock: the
// instant a concurrent reaper can take it. A seam so tests can force that race
// instead of waiting for it.
var scratchCreated = func(dir string) {}

// scratchOpened runs between the owner opening its lock file and locking it:
// the instant it can hold an open handle on a dir a reaper is deleting under
// its lock. A seam so tests can run the reaper's delete exactly there.
var scratchOpened = func(dir string) {}

// scratchReapRemove is the reaper's delete of a dead owner's dir, made while
// it HOLDS that dir's lock: the instant an owner can open the lock file of a
// dir about to vanish. A seam so tests can run an owner inside that window.
var scratchReapRemove = os.RemoveAll

// ownedScratch is an ephemeral directory held live by its owner's lock.
type ownedScratch struct {
	dir  string
	lock *os.File // the held-locked handle; closing it releases the lock
}

// newOwnedScratch reaps every dead owner's prefix-named scratch under parent,
// then creates and locks a fresh one. The dir's name carries prefix, as
// os.MkdirTemp makes it.
//
// There is an unavoidable instant between creating the dir and locking it, in
// which a concurrent reaper sees an unlocked dir and takes it. The protocol
// makes that loss detectable rather than silent: the reaper deletes only while
// HOLDING the lock, so this owner's open fails outright (the dir is already
// gone), or its lock is granted only after the delete. On unix that grant is
// on a lock file no longer at its path, and the owner retries with a new dir.
// On Windows the delete cannot remove a lock file the owner already has open,
// so the owner keeps a dir that still exists. Either way it never proceeds in
// a deleted one.
func newOwnedScratch(parent, prefix string) (*ownedScratch, error) {
	reapDeadScratch(parent, prefix)
	for range ownedScratchAttempts {
		dir, err := os.MkdirTemp(parent, prefix)
		if err != nil {
			// dir is normally "" here — defensive against a mutant flipping
			// this check and orphaning a dir MkdirTemp actually created.
			_ = os.RemoveAll(dir)
			return nil, err
		}
		scratchCreated(dir)
		lockPath := filepath.Join(dir, ownedScratchLockName)
		f, err := iox.OpenLockFile(lockPath, ownedScratchLockMode)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("open scratch lock %s: %w", dir, err)
		}
		scratchOpened(dir)
		if err := lockOwnerFile(f); err != nil {
			_ = f.Close()
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("lock scratch %s: %w", dir, err)
		}
		if _, err := os.Lstat(lockPath); err == nil {
			return &ownedScratch{dir: dir, lock: f}, nil
		}
		_ = f.Close()
	}
	return nil, fmt.Errorf("scratch under %s: reaped before it could be claimed, %d times", parent, ownedScratchAttempts)
}

// release drops the lock and removes the scratch. Unlocking FIRST is safe
// because the owner is finished with the dir, so a reaper taking it in
// between only repeats the removal; and it lets the removal succeed where an
// open file cannot be unlinked.
func (s *ownedScratch) release() {
	_ = s.lock.Close()
	_ = os.RemoveAll(s.dir)
}

// reapDeadScratch removes the prefix-named scratch dirs under parent whose
// owner is gone — its lock is obtainable — and leaves every live one alone.
// Only this user's own directories are candidates: parent may be the shared
// OS temp dir, where another account's lookalike dir is not ours to open,
// let alone delete.
//
// Best-effort: an unreadable parent or an unremovable dir is left for the
// next owner to reap, because creating scratch must never fail over a
// janitor.
func reapDeadScratch(parent, prefix string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !ownedByCurrentUser(info) {
			continue
		}
		dir := filepath.Join(parent, e.Name())
		fl := flock.New(filepath.Join(dir, ownedScratchLockName), flock.SetPermissions(ownedScratchLockMode))
		if locked, err := fl.TryLock(); err != nil || !locked {
			continue
		}
		// Removed while HOLDING the lock: an owner that created this dir but
		// had not yet locked it is kept out until the dir is gone, which is
		// what lets it detect the loss (newOwnedScratch). The second pass,
		// after the unlock, finishes the job where the held lock file itself
		// could not be unlinked while open.
		_ = scratchReapRemove(dir)
		_ = fl.Unlock()
		_ = os.RemoveAll(dir)
	}
}
