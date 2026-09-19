package operations

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestAssignSessionHarp_HoldsTheLivenessLockUntilEndSession pins the one
// wiring every session-owning path shares: minting a harp holds its liveness
// lock in THIS process, and ending the session lets it go. Every consumer of
// the lock — the engine-home sweep, the worktree sweep, clean — reads a
// session as alive between those two calls and as dead after the second.
//
// MUTATION: drop the Hold from AssignSessionHarp → the first assertion goes
// red; drop the Release from EndSession → the second does.
func TestAssignSessionHarp_HoldsTheLivenessLockUntilEndSession(t *testing.T) {
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	t.Cleanup(func() { sessionlock.Release(entry.HarpName) })

	p := sessionlock.Inspect(entry.HarpName)
	assert.Equal(t, sessionlock.Alive, p.Verdict, "a minted harp is held by the minting process")
	assert.Equal(t, os.Getpid(), p.PID)

	lock, err := paths.HarpLockPath(entry.HarpName)
	require.NoError(t, err)
	assert.FileExists(t, lock)

	require.NoError(t, EndSession(entry.HarpName, time.Now()))
	assert.Equal(t, sessionlock.Dead, sessionlock.Inspect(entry.HarpName).Verdict, "an ended session's lock is free")
	assert.FileExists(t, lock, "the file stays: unlocked is the dead signal, and its pid is for a human")
}

// TestEndSession_ReleasesTheLockEvenWhenTheEndMarkFails: from the owner's
// side the session HAS ended whatever the index said, so the lock must not
// outlive it — a lock held on by a long-lived parent for a child whose
// end-mark failed would read that child as alive for the parent's whole life.
// The failing mark here is a harp the index never carried: held directly,
// never minted, so MarkEnded refuses it.
func TestEndSession_ReleasesTheLockEvenWhenTheEndMarkFails(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "unminted-harp"
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
	require.Equal(t, sessionlock.Alive, sessionlock.Inspect(harp).Verdict)

	require.Error(t, EndSession(harp, time.Now()), "precondition: the end-mark fails for a harp the index does not carry")
	assert.Equal(t, sessionlock.Dead, sessionlock.Inspect(harp).Verdict)
}
