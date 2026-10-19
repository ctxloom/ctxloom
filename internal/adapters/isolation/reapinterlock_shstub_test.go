//go:build !windows

// Drives hermeticHostContainer's #!/bin/sh runtime stub, which a Windows host cannot exec.

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// interlockCell isolates HOME, shortens the interlock wait to wait, and
// returns a hermetic container ready to prepare and the reap interlock's
// path.
func interlockCell(t *testing.T, wait time.Duration) (Container, string) {
	t.Helper()
	testsupport.Isolate(t)
	prev := sessions.ReapInterlockWait
	sessions.ReapInterlockWait = wait
	t.Cleanup(func() { sessions.ReapInterlockWait = prev })
	interlock, err := paths.LockReapInterlockPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(interlock), 0o755))
	c := hermeticHostContainer(t, nil)
	c.state = SessionState{Harp: "brisk-teal-otter"}
	return c, interlock
}

// takeExclusively is a reap's take of the interlock: one attempt.
func takeExclusively(path string) (safefs.Lock, error) {
	once, cancel := context.WithCancel(context.Background())
	cancel()
	return safefs.New().Locks.TryLock(once, path)
}

// A reap in progress holds the interlock exclusive: a container launch waits
// for it, for the bounded wait, and is then refused — it never binds a host
// lock sidecar without its shared hold.
func TestContainerLaunch_WaitsOutThenRefusesAReapInProgress(t *testing.T) {
	const wait = 150 * time.Millisecond
	c, interlock := interlockCell(t, wait)
	reap, err := takeExclusively(interlock)
	require.NoError(t, err)
	defer func() { _ = reap.Unlock() }()

	start := time.Now()
	ws, err := c.prepareWorkspace(context.Background(), t.TempDir(), "member-reap")
	if ws != nil {
		_ = ws.Cleanup()
	}

	require.ErrorIs(t, err, sessions.ErrLockReapInProgress, "the launch proceeded during a reap")
	assert.GreaterOrEqual(t, time.Since(start), wait, "the launch did not wait for the reap")
}

// A launch holds the interlock shared from its bind until its Cleanup, after
// the container is gone: a reap asking in between is refused, and gets it
// once the launch has cleaned up.
func TestContainerLaunch_HoldsTheInterlockUntilCleanup(t *testing.T) {
	c, interlock := interlockCell(t, time.Second)

	ws, err := c.prepareWorkspace(context.Background(), t.TempDir(), "member-hold")
	require.NoError(t, err)
	_, err = takeExclusively(interlock)
	require.ErrorIs(t, err, safefs.ErrLockHeld, "a reap got the interlock while a launch held host binds")

	require.NoError(t, ws.Cleanup())
	reap, err := takeExclusively(interlock)
	require.NoError(t, err, "Cleanup left the interlock held")
	require.NoError(t, reap.Unlock())
}
