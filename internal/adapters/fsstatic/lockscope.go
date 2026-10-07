package fsstatic

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// deliveryLockName is the lock, in the home lock directory, that every
// static delivery and reversal holds for its whole run. It cannot collide
// with a lock paths.HomePathFor names: those are all flattened paths
// (paths.FlatName), which always carry a hash.
const deliveryLockName = "static-delivery.lock"

// lockScope is every lock one delivery or reversal takes, held from when it
// is taken until the run has committed. A delivery reads before it writes —
// its writer's previous files from the record, a managed dir's ledger — and
// its writes land only at its batch's commit; a lock released in between
// lets another writer land in that window, and the delivery then commits
// what it read before that writer's files existed.
//
// LOCK ORDER. Every lock a run takes is taken in this order:
//
//  1. the delivery lock (deliveryLockName), first, by every Deliver and
//     Reverse;
//  2. the locks its approaches take through the Root they are handed — a
//     managed dir's lock with its ledger marker's nested inside it
//     (agent.WriteManagedPackageFiles, ledger.Ledger.Write) — in the order
//     the approaches take them;
//  3. its batch's per-file locks, in the batch's sorted order
//     (safefs.Batch.Commit).
//
// A lock the run already holds is granted again without being taken, never
// waited on: the batch writes the ledger marker whose lock step 2 holds, and
// two approaches may share a dir.
//
// Steps 2 and 3 follow no order two runs would agree on. That is safe ONLY
// because step 1 admits one run at a time: every other taker of those locks
// holds a single dir lock with its marker lock inside it, or a single file's
// lock (sessions.WithFileLock), and waits on nothing a run holds while it
// holds one of them. A taker outside a run that holds one of these locks and
// then waits on another breaks this order.
type lockScope struct {
	locks safefs.Locks
	mu    sync.Mutex
	held  map[string]safefs.Lock
	order []string
}

var _ safefs.Locks = (*lockScope)(nil)

// begin takes the delivery lock and returns the scope holding it.
func (s *Static) begin() (*lockScope, error) {
	dir, err := paths.HomeLocksDir()
	if err != nil {
		return nil, fmt.Errorf("fsstatic: the delivery lock: %w", err)
	}
	sc := &lockScope{locks: s.locks, held: map[string]safefs.Lock{}}
	if _, err := sc.Lock(filepath.Join(dir, deliveryLockName)); err != nil {
		return nil, err
	}
	return sc, nil
}

// Lock takes path's lock for the rest of the run.
func (sc *lockScope) Lock(path string) (safefs.Lock, error) {
	return sc.take(path, func() (safefs.Lock, error) { return sc.locks.Lock(path) })
}

// RLock takes path's lock EXCLUSIVELY for the rest of the run: a lock the
// run holds may be asked for again in either kind, and a shared hold could
// not be upgraded.
func (sc *lockScope) RLock(path string) (safefs.Lock, error) { return sc.Lock(path) }

// TryLock takes path's lock for the rest of the run if it can by the time
// ctx is done.
func (sc *lockScope) TryLock(ctx context.Context, path string) (safefs.Lock, error) {
	return sc.take(path, func() (safefs.Lock, error) { return sc.locks.TryLock(ctx, path) })
}

// Held probes path's lock as any other taker would; one the run holds is
// held.
func (sc *lockScope) Held(path string) (bool, error) { return sc.locks.Held(path) }

func (sc *lockScope) take(path string, take func() (safefs.Lock, error)) (safefs.Lock, error) {
	key := filepath.Clean(path)
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if lk, ok := sc.held[key]; ok {
		return scopedLock{lk}, nil
	}
	lk, err := take()
	if err != nil {
		return nil, err
	}
	sc.held[key] = lk
	sc.order = append(sc.order, key)
	return scopedLock{lk}, nil
}

// release unlocks every lock the run took, last taken first.
func (sc *lockScope) release() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var errs []error
	for i := len(sc.order) - 1; i >= 0; i-- {
		if err := sc.held[sc.order[i]].Unlock(); err != nil {
			errs = append(errs, fmt.Errorf("fsstatic: release %s: %w", sc.order[i], err))
		}
	}
	sc.held, sc.order = map[string]safefs.Lock{}, nil
	return errors.Join(errs...)
}

// scopedLock is a lock as a run hands it out: its Unlock leaves it held,
// because the run releases it after its commit.
type scopedLock struct{ safefs.Lock }

func (scopedLock) Unlock() error { return nil }
