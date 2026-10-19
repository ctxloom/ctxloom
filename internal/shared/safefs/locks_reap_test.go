package safefs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A lock some taker holds is never reaped: the reap is one non-blocking
// attempt, refused, and the holder's file stays the one at the path.
func TestReapLockFile_LeavesAHeldLock(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			held, err := r.locks.Lock(r.path)
			require.NoError(t, err)
			defer func() { _ = held.Unlock() }()

			reaped, err := ReapLockFile(r.locks, r.path)
			require.NoError(t, err)
			assert.False(t, reaped)
			assert.True(t, r.exists(), "a held lock file was removed")
			assert.True(t, held.Current(), "the holder's file is no longer the one at the path")
		})
	}
}

// A lock nobody holds is reaped: its file is gone, and the reaper let go of
// it, so the next taker takes the fresh file at once.
func TestReapLockFile_RemovesAFreeLock(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			lk, err := r.locks.Lock(r.path)
			require.NoError(t, err)
			require.NoError(t, lk.Unlock())

			reaped, err := ReapLockFile(r.locks, r.path)
			require.NoError(t, err)
			assert.True(t, reaped)
			assert.False(t, r.exists(), "the free lock file is still there")

			next, err := r.locks.TryLock(expired(), r.path)
			require.NoError(t, err, "the reaper still holds what it reaped")
			require.NoError(t, next.Unlock())
		})
	}
}

// Reaping a lock file that does not exist does nothing, and creates nothing.
func TestReapLockFile_AnAbsentLockIsANoOp(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			reaped, err := ReapLockFile(r.locks, r.path)
			require.NoError(t, err)
			assert.False(t, reaped)
			assert.False(t, r.exists(), "the reap created the lock file")
		})
	}
}

// TestLocks_TakersNeverHoldTogetherWhileAReaperRuns: every taker of one lock,
// blocking and polling, against a reaper removing its file whenever it is
// free. Each holder counts itself in while it holds; the count never passes
// one. A taker that kept a lock granted on a file the reaper had already
// unlinked would hold it beside the next taker of the fresh file.
//
// The reaper pauses between reaps: one that never paused would win every
// release, and replace a waiting taker's file on every attempt it has — a
// removal in a loop, which is what ErrLockReplaced refuses.
func TestLocks_TakersNeverHoldTogetherWhileAReaperRuns(t *testing.T) {
	const takers, rounds = 4, 150
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			var holders, overlaps, reaps atomic.Int32
			stop := make(chan struct{})
			reaperDone := make(chan struct{})
			go func() {
				defer close(reaperDone)
				for {
					select {
					case <-stop:
						return
					default:
					}
					reaped, err := ReapLockFile(r.locks, r.path)
					if err != nil {
						t.Errorf("reap: %v", err)
						return
					}
					if reaped {
						reaps.Add(1)
					}
					time.Sleep(100 * time.Microsecond)
				}
			}()

			var wg sync.WaitGroup
			for i := range takers {
				take := func() (Lock, error) { return r.locks.Lock(r.path) }
				if i%2 == 1 {
					take = func() (Lock, error) { return r.locks.TryLock(context.Background(), r.path) }
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range rounds {
						lk, err := take()
						if err != nil {
							t.Errorf("take: %v", err)
							return
						}
						if holders.Add(1) > 1 {
							overlaps.Add(1)
						}
						time.Sleep(20 * time.Microsecond)
						holders.Add(-1)
						_ = lk.Unlock()
					}
				}()
			}
			wg.Wait()
			close(stop)
			<-reaperDone

			assert.Zero(t, overlaps.Load(), "two takers held one lock at once")
			assert.Positive(t, reaps.Load(), "the reaper never reaped, so nothing was raced")
		})
	}
}

// replacedLocks hands out, for its first stale takes, a lock whose file is no
// longer at its path, as a Locks wrapping another without re-checking would.
type replacedLocks struct {
	Locks
	stale    int
	released int
}

func (r *replacedLocks) Lock(path string) (Lock, error) {
	lk, err := r.Locks.Lock(path)
	if err != nil || r.stale == 0 {
		return lk, err
	}
	r.stale--
	return replacedLock{Lock: lk, r: r}, nil
}

type replacedLock struct {
	Lock
	r *replacedLocks
}

func (replacedLock) Current() bool { return false }

func (l replacedLock) Unlock() error {
	l.r.released++
	return l.Lock.Unlock()
}

// WithLock re-checks the lock it is handed: a lock on a replaced file is let
// go and taken again, and fn runs once, under a lock on the file at the path.
func TestWithLock_RetakesALockOnAReplacedFile(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			locks := &replacedLocks{Locks: r.locks, stale: 2}
			runs := 0
			require.NoError(t, WithLock(locks, r.path, func() error {
				runs++
				return nil
			}))
			assert.Equal(t, 1, runs)
			assert.Equal(t, 2, locks.released, "a lock on a replaced file was kept")
		})
	}
}

// A lock whose file is replaced on every take is refused once the attempts
// run out, and fn never runs.
func TestWithLock_RefusesALockReplacedOnEveryTake(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			locks := &replacedLocks{Locks: r.locks, stale: lockAttempts}
			err := WithLock(locks, r.path, func() error {
				t.Fatal("fn ran under a lock on a replaced file")
				return nil
			})
			require.ErrorIs(t, err, ErrLockReplaced)
			assert.Equal(t, lockAttempts, locks.released)
		})
	}
}
