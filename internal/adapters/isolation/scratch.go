package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// ownedScratchLockName is the lock file inside every owned scratch dir. The
// owner holds an exclusive flock on it for the scratch's whole life, and the
// kernel drops that lock when the owner exits by ANY route — a SIGKILL or an
// OOM included, where no deferred cleanup runs. So "the lock is obtainable"
// is exactly "the owner is gone", which is the liveness answer a reaper
// needs: an age cutoff can only guess at it, and a PID check is wrong across
// PID namespaces sharing one filesystem. flock conflicts per open file
// description, so the answer is also right between goroutines of one process.
const ownedScratchLockName = ".ctxloom-owner.lock"

// ownedScratchLockMode keeps the lock file owner-only: opening it is what
// takes the lock, so its readers are exactly who can hold the scratch live.
const ownedScratchLockMode = 0o600

// ownedScratchAttempts bounds creation retries. A retry happens only when a
// concurrent reaper took the freshly made dir in the instant between its
// creation and its lock — see newOwnedScratch.
const ownedScratchAttempts = 3

// scratchCreated runs between a scratch dir's creation and its lock: the
// instant a concurrent reaper can take it. A seam so tests can force that race
// instead of waiting for it.
var scratchCreated = func(dir string) {}

// scratchLocked runs once the owner holds its lock and before it confirms the
// lock file is still at its path. An owner that opened its lock file inside a
// reaper's hold reaches this point holding a lock on a file the reaper has
// since unlinked. A seam so tests can put the owner in that state on any
// platform: flock.Lock opens and locks in one call, so the open itself cannot
// be intercepted without a kernel file-event watch.
var scratchLocked = func(dir string) {}

// scratchReapRemove is the reaper's delete of a dead owner's dir, made while
// it HOLDS that dir's lock: the instant an owner can open the lock file of a
// dir about to vanish. A seam so tests can run an owner inside that window.
var scratchReapRemove = os.RemoveAll

// ownedScratch is an ephemeral directory held live by its owner's lock.
type ownedScratch struct {
	dir  string
	lock *flock.Flock
}

// newOwnedScratch reaps every dead owner's prefix-named scratch under parent,
// then creates and locks a fresh one. The dir's name carries prefix, as
// os.MkdirTemp makes it.
//
// There is an unavoidable instant between creating the dir and locking it, in
// which a concurrent reaper sees an unlocked dir and takes it. The protocol
// makes that loss detectable rather than silent: the reaper deletes only while
// HOLDING the lock, so this owner's Lock either fails outright (the dir is
// already gone) or succeeds only after the delete, on a lock file no longer at
// its path. Either way the owner retries with a new dir, never proceeding in
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
		fl := flock.New(lockPath, flock.SetPermissions(ownedScratchLockMode))
		if err := fl.Lock(); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("lock scratch %s: %w", dir, err)
		}
		scratchLocked(dir)
		if _, err := os.Lstat(lockPath); err == nil {
			return &ownedScratch{dir: dir, lock: fl}, nil
		}
		_ = fl.Unlock()
	}
	return nil, fmt.Errorf("scratch under %s: reaped before it could be claimed, %d times", parent, ownedScratchAttempts)
}

// release removes the scratch and drops the lock. Unlocking FIRST is safe
// because the owner is finished with the dir, so a reaper taking it in
// between only repeats the removal; and it lets the removal succeed where an
// open file cannot be unlinked.
func (s *ownedScratch) release() {
	_ = s.lock.Unlock()
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
