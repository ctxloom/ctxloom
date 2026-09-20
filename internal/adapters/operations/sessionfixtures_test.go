package operations

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// liveSession registers harp in the session index for projectDir and leaves it
// UNENDED — the index's own definition of a live session.
func liveSession(t *testing.T, projectDir string) string {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	return e.HarpName
}

func markEnded(t *testing.T, harp string) {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	require.NoError(t, mgr.MarkEnded(harp, time.Now()))
}

// crashedSession registers harp UNENDED in the index — exactly what a session
// whose process died before EndSession leaves behind — with a liveness lock
// file that nothing holds. Hold-then-Release produces the identical on-disk
// state a SIGKILL does (the kernel drops the lock; the file and its pid
// remain), without needing a process to kill.
func crashedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
	return harp
}

// endedSession registers harp, marks it ended, and leaves the lock released
// — the on-disk state a session leaves when it reached EndSession.
func endedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := crashedSession(t, projectDir)
	markEnded(t, harp)
	return harp
}

// locklessEndedSession registers harp and marks it ended WITHOUT ever holding
// its lock: a session from before the lock existed. Nothing can prove its
// owner dead.
func locklessEndedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	markEnded(t, harp)
	return harp
}

// resumedSession registers harp, marks it ended, and then HOLDS its lock for
// the rest of the test — a session resumed under its harp: EndedAt is still
// set from the earlier end, and its owner is running right now.
func resumedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	markEnded(t, harp)
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
	return harp
}
