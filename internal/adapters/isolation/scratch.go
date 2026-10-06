package isolation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// ownedScratchLockName is the lock file inside every owned scratch dir. The
// owner holds an exclusive lock on it (safefs.Locks) for the scratch's whole
// life, and the kernel drops that lock when the owner exits by ANY route — a
// SIGKILL or an OOM included, where no deferred cleanup runs. So "the lock is
// obtainable" is exactly "the owner is gone", which is the liveness answer a
// reaper needs: an age cutoff can only guess at it, and a PID check is wrong
// across PID namespaces sharing one filesystem. The lock conflicts per open
// handle, so the answer is also right between goroutines of one process.
const ownedScratchLockName = ".ctxloom-owner.lock"

// ownedScratchAttempts bounds creation retries. A retry happens only when a
// concurrent reaper took the freshly made dir in the instant between its
// creation and its lock — see newOwnedScratch.
const ownedScratchAttempts = 3

// scratchCreated runs between a scratch dir's creation and its lock: the
// instant a concurrent reaper can take it. A seam so tests can force that race
// instead of waiting for it.
var scratchCreated = func(dir string) {}

// scratchContended runs when the owner's first attempt at its lock finds a
// reaper holding it, before the owner waits the reaper out. A seam so tests
// can run the reaper's delete exactly there.
var scratchContended = func(dir string) {}

// scratchLocked runs once the owner is granted its lock, before it checks the
// lock is on the file still at its path (Lock.Current): the instant a delete
// made between the owner's open and its grant shows. A seam so tests can make
// that delete.
var scratchLocked = func(dir string) {}

// scratchReapRemove is the reaper's delete of a dead owner's dir, made while
// it HOLDS that dir's lock: the instant an owner can open the lock file of a
// dir about to vanish. A seam so tests can run an owner inside that window.
var scratchReapRemove = os.RemoveAll

// ownedScratch is an ephemeral directory held live by its owner's lock.
type ownedScratch struct {
	dir  string
	lock safefs.Lock // released by release
}

// newOwnedScratch reaps every dead owner's prefix-named scratch under parent,
// then creates and locks a fresh one. The dir's name carries prefix, as
// os.MkdirTemp makes it.
//
// The scratch is owner-only (safefs Private), lock file included: a lock is taken
// on an open handle, so whoever can open the lock file can hold the scratch
// live. It is restricted only once it is held (claim): before that a
// concurrent reaper may have removed it, and restricting would recreate it.
//
// There is an unavoidable instant between creating the dir and locking it, in
// which a concurrent reaper sees an unlocked dir and takes it. The protocol
// makes that loss detectable rather than silent: the reaper deletes only while
// HOLDING the lock, so this owner's attempt finds the dir gone
// (fs.ErrNotExist — TryLock never recreates it), or its lock is granted on a
// file no longer at its path (Lock.Current); either way the owner retries
// with a new dir. On Windows the delete cannot remove a lock file the owner
// has open, so an owner holding it keeps a dir that still exists. It never
// proceeds in a deleted one.
func newOwnedScratch(parent, prefix string) (*ownedScratch, error) {
	root := safefs.New()
	reapDeadScratch(root.Locks, parent, prefix)
	for range ownedScratchAttempts {
		dir, err := os.MkdirTemp(parent, prefix)
		if err != nil {
			// dir is normally "" here — defensive against a mutant flipping
			// this check and orphaning a dir MkdirTemp actually created.
			_ = os.RemoveAll(dir)
			return nil, err
		}
		scratchCreated(dir)
		lock, err := lockScratch(root.Locks, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("lock scratch %s: %w", dir, err)
		}
		scratchLocked(dir)
		if lock.Current() {
			return claim(root.Private, dir, lock)
		}
		_ = lock.Unlock()
	}
	return nil, fmt.Errorf("scratch under %s: reaped before it could be claimed, %d times", parent, ownedScratchAttempts)
}

// lockScratch takes dir's owner lock: one attempt, then — a reaper holds it
// (scratchContended) — a wait for as long as the reaper holds it.
func lockScratch(locks safefs.Locks, dir string) (safefs.Lock, error) {
	lockPath := filepath.Join(dir, ownedScratchLockName)
	lock, err := locks.TryLock(doneContext(), lockPath)
	if errors.Is(err, safefs.ErrLockHeld) {
		scratchContended(dir)
		lock, err = locks.TryLock(context.Background(), lockPath)
	}
	return lock, err
}

// doneContext is a context already done: a TryLock given it makes exactly
// one attempt.
func doneContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// claim makes a scratch this owner holds locked owner-only. On Windows a
// restriction propagates to the lock file, created before it, with the rest
// of the directory.
func claim(private safefs.Private, dir string, lock safefs.Lock) (*ownedScratch, error) {
	s := &ownedScratch{dir: dir, lock: lock}
	if err := private.Ensure(dir); err != nil {
		s.release()
		return nil, fmt.Errorf("restrict scratch %s to its owner: %w", dir, err)
	}
	return s, nil
}

// release drops the lock and removes the scratch. Unlocking FIRST is safe
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
func reapDeadScratch(locks safefs.Locks, parent, prefix string) {
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
		lock, err := locks.TryLock(doneContext(), filepath.Join(dir, ownedScratchLockName))
		if err != nil {
			continue
		}
		// Removed while HOLDING the lock: an owner that created this dir but
		// had not yet locked it is kept out until the dir is gone, which is
		// what lets it detect the loss (newOwnedScratch). The second pass,
		// after the unlock, finishes the job where the held lock file itself
		// could not be unlinked while open.
		_ = scratchReapRemove(dir)
		_ = lock.Unlock()
		_ = os.RemoveAll(dir)
	}
}
