package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// scratchDirs lists the directories directly under parent carrying prefix.
func scratchDirs(t *testing.T, parent, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(parent, e.Name()))
		}
	}
	return out
}

// TestNewOwnedScratch_ReclaimsAfterReaperWinsClaimRace: a reaper can see a
// dir in the instant between its creation and its owner's lock, find it
// unlocked, and delete it. The owner must notice and claim a fresh dir — never
// carry on in a deleted one. The race is forced through the scratchCreated
// seam: the reaper runs exactly in that instant, once.
func TestNewOwnedScratch_ReclaimsAfterReaperWinsClaimRace(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-claimrace-"
	var raced []string
	orig := scratchCreated
	scratchCreated = func(dir string) {
		if len(raced) == 0 {
			raced = append(raced, dir)
			reapDeadScratch(safefs.New().Locks, parent, prefix)
		}
	}
	t.Cleanup(func() { scratchCreated = orig })

	s, err := newOwnedScratch(parent, prefix)
	require.NoError(t, err)
	t.Cleanup(s.release)

	require.Len(t, raced, 1)
	assert.NoDirExists(t, raced[0], "precondition: the reaper took the first dir")
	assert.NotEqual(t, raced[0], s.dir)
	assert.DirExists(t, s.dir, "the owner claimed a dir that exists")
	reapDeadScratch(safefs.New().Locks, parent, prefix)
	assert.DirExists(t, s.dir, "and holds it live against the next reaper")
}

// TestReapDeadScratch_DeletesOnlyWhileHoldingLock: the reaper deletes a dead
// owner's dir while still HOLDING that dir's lock. An owner that opened the
// lock file just before is then kept out until the dir is gone, which is what
// lets it detect the loss. The scratchReapRemove seam checks, at the moment of
// the delete, that nobody else can take the lock.
func TestReapDeadScratch_DeletesOnlyWhileHoldingLock(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-reaphold-"
	dead := filepath.Join(parent, prefix+"dead")
	require.NoError(t, os.Mkdir(dead, 0o700))

	var (
		removed     []string
		probeLocked bool
		probeErr    error
	)
	orig := scratchReapRemove
	scratchReapRemove = func(dir string) error {
		removed = append(removed, dir)
		var held bool
		held, probeErr = safefs.New().Locks.Held(filepath.Join(dir, ownedScratchLockName))
		probeLocked = !held
		return os.RemoveAll(dir)
	}
	t.Cleanup(func() { scratchReapRemove = orig })

	reapDeadScratch(safefs.New().Locks, parent, prefix)

	require.Equal(t, []string{dead}, removed, "precondition: the reaper took the dead dir")
	require.NoError(t, probeErr)
	assert.False(t, probeLocked, "the reaper holds the owner lock at the moment it deletes")
	assert.NoDirExists(t, dead)
}

// raceStepTimeout bounds each wait in a forced interleaving, so a seam that
// is never reached fails the test instead of hanging it.
const raceStepTimeout = 10 * time.Second

func awaitStep(ch <-chan struct{}, what string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(raceStepTimeout):
		return fmt.Errorf("timed out waiting for %s", what)
	}
}

// TestNewOwnedScratch_OwnerContendingInsideReapersHoldNeverContinuesInDeletedDir:
// the owner tries its lock AFTER a reaper has locked the dir but BEFORE the
// reaper deletes it, then waits only once the reaper is completely done.
// Whatever the platform makes of that, the owner must end up holding a lock
// on a dir that exists, with nothing left behind.
//
// Forced, not waited for: scratchCreated runs the reaper up to its delete and
// parks it there, holding the lock; the owner's first attempt meets that hold
// (scratchContended), which releases the delete, and the owner then waits for
// the reaper to finish its delete, unlock and second pass before it retries.
func TestNewOwnedScratch_OwnerContendingInsideReapersHoldNeverContinuesInDeletedDir(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-holdrace-"

	var (
		raced                  string
		contended              bool
		probeLocked            bool
		probeErr, removeErr    error
		holdErr, contendErr    error
		reaperErr              error
		inHold, ownerContended = make(chan struct{}), make(chan struct{})
		reaperDone             = make(chan struct{})
	)
	origCreated, origContended, origRemove := scratchCreated, scratchContended, scratchReapRemove
	restore := func() {
		scratchCreated, scratchContended, scratchReapRemove = origCreated, origContended, origRemove
	}
	t.Cleanup(restore)

	scratchReapRemove = func(dir string) error {
		// Nobody but the reaper may be able to take the lock here.
		var held bool
		held, probeErr = safefs.New().Locks.Held(filepath.Join(dir, ownedScratchLockName))
		probeLocked = !held
		close(inHold)
		contendErr = awaitStep(ownerContended, "the owner to meet the reaper's hold")
		removeErr = os.RemoveAll(dir)
		return removeErr
	}
	scratchCreated = func(dir string) {
		if raced != "" {
			return
		}
		raced = dir
		go func() { defer close(reaperDone); reapDeadScratch(safefs.New().Locks, parent, prefix) }()
		holdErr = awaitStep(inHold, "the reaper to reach its delete")
	}
	scratchContended = func(dir string) {
		if dir != raced || contended {
			return
		}
		contended = true
		close(ownerContended)
		reaperErr = awaitStep(reaperDone, "the reaper to finish")
	}

	s, err := newOwnedScratch(parent, prefix)
	require.NoError(t, awaitStep(reaperDone, "the reaper to finish"))
	restore()
	require.NoError(t, err)
	t.Cleanup(s.release)
	require.NoError(t, holdErr)
	require.NoError(t, contendErr)
	require.NoError(t, reaperErr)

	require.NoError(t, probeErr)
	assert.False(t, probeLocked, "the reaper holds the owner lock at the moment it deletes")
	lockPath := filepath.Join(s.dir, ownedScratchLockName)
	assert.DirExists(t, s.dir, "the owner never continues in a deleted dir")
	assert.FileExists(t, lockPath)
	held, err := safefs.New().Locks.Held(lockPath)
	require.NoError(t, err)
	assert.True(t, held, "the owner really holds the lock at its path")
	assert.Equal(t, []string{s.dir}, scratchDirs(t, parent, prefix), "nothing left behind")
	// Not vacuous on either platform: the reaper's delete either failed and the
	// owner kept a live dir, or succeeded and the owner moved on.
	if s.dir == raced {
		require.Error(t, removeErr, "the owner kept the raced dir, so the reaper's delete must have failed")
	} else {
		assert.NoDirExists(t, raced, "the reaper took the raced dir")
	}
	reapDeadScratch(safefs.New().Locks, parent, prefix)
	assert.DirExists(t, s.dir, "and the owner holds it live against the next reaper")
}

// A claimed scratch is owner-only: whoever can open its lock file can hold
// the scratch live, and the lock file is reached only through the dir — on
// unix its mode blocks traversal, on Windows the lock file inherits its DACL.
func TestNewOwnedScratch_IsOwnerOnly(t *testing.T) {
	s, err := newOwnedScratch(t.TempDir(), "ctxloom-owneronly-")
	require.NoError(t, err)
	t.Cleanup(s.release)

	require.NoError(t, safefs.New().Private.Check(s.dir))
}
