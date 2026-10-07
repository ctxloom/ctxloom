package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// grantedShared runs onShared each time a shared lock is granted, while it
// is still held.
type grantedShared struct {
	safefs.Locks
	onShared func(path string)
}

func (g grantedShared) RLock(path string) (safefs.Lock, error) {
	lk, err := g.Locks.RLock(path)
	if err == nil {
		g.onShared(path)
	}
	return lk, err
}

// TestReads_HoldASharedLockThatExcludesAWriter: a read folds under the log's
// shared lock, so a writer — another process's exclusive attempt on the same
// lock file — is refused while it runs. Probed at the moment the read's lock
// is granted, with one attempt, so nothing waits.
func TestReads_HoldASharedLockThatExcludesAWriter(t *testing.T) {
	s := newLog(t, "sess")
	if _, err := s.AddWithTrigger("work", "", ""); err != nil {
		t.Fatal(err)
	}
	var probes []error
	s.log.locks = grantedShared{Locks: s.log.locks, onShared: func(path string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		w, err := safefs.New().Locks.TryLock(ctx, path)
		if err == nil {
			_ = w.Unlock()
		}
		probes = append(probes, err)
	}}
	if _, err := s.List(nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(probes) == 0 {
		t.Fatal("the read took no shared lock")
	}
	for _, err := range probes {
		if !errors.Is(err, safefs.ErrLockHeld) {
			t.Fatalf("a writer was let in while a read held the log: %v", err)
		}
	}
}
