package countersign

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// waitBound caps how long the test waits for the notice. It bounds a
// regression only; a passing run returns as soon as lockwait.After elapses.
const waitBound = lockwait.After + 30*time.Second

// TestLockedIndexUpdate_ReportsAWaitOnAHeldLock: the one lock that serializes
// recorded approvals and rejections must not block silently. The wait is
// FORCED by holding the index lock here, never raced: the update cannot
// proceed until this test releases it, and the test releases it only once
// the lock-wait record naming that lock has appeared (or the bound expires).
func TestLockedIndexUpdate_ReportsAWaitOnAHeldLock(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	t.Cleanup(zap.ReplaceGlobals(zap.New(core)))

	s := NewStore(t.TempDir(), afero.NewOsFs())
	lockPath, err := indexLockPath(s.indexPath())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	held := flock.New(lockPath)
	require.NoError(t, held.Lock())
	released := false
	t.Cleanup(func() {
		if !released {
			_ = held.Unlock()
		}
	})

	ran := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.lockedIndexUpdate(func() error { close(ran); return nil })
	}()

	waitedOnIndex := func() bool {
		for _, e := range logs.FilterMessage(lockwait.LogWaitExceeded).All() {
			if e.ContextMap()["path"] == lockPath {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(waitBound)
	for !waitedOnIndex() && time.Now().Before(deadline) {
		select {
		case <-ran:
			t.Fatal("the update ran while the index lock was held by another holder")
		case <-time.After(50 * time.Millisecond):
		}
	}
	notified := waitedOnIndex()

	require.NoError(t, held.Unlock())
	released = true
	require.NoError(t, <-done)
	require.True(t, notified,
		"a countersignature index update blocked on a held lock for %s reported no %q record for %s",
		waitBound, lockwait.LogWaitExceeded, lockPath)
}
