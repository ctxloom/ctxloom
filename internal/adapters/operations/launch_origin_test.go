package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The mint is the one place that knows who a session is for, so it is the
// one place that records it: a sweep in a later process decides whether an
// undistilled session may be purged by this stamp alone.
func TestMintIdentity_StampsTheSeedsOrigin(t *testing.T) {
	testsupport.Isolate(t)
	for _, tc := range []struct {
		seed sessions.Seed
		want sessions.Origin
	}{
		{sessions.Seed{ProjectDir: "/proj"}, sessions.OriginSession},
		{sessions.Seed{ProjectDir: "/proj", Depth: 1}, sessions.OriginAgent},
		{sessions.Seed{ProjectDir: "/proj", OneShot: true}, sessions.OriginOneShot},
	} {
		store := sessions.NewMemStore()
		id, err := MintIdentity(store, tc.seed, t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { sessionlock.Release(id.Harp) })
		got, err := store.Find(id.Harp)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got.Origin)
	}
}

// The liveness lock guards a session's ON-DISK data from being reaped as
// crashed. A session minted into the in-memory store (`run --dry-run`'s
// preview) has none, so it takes no lock: otherwise every preview leaves a
// <harp>.lock under ~/.ctxloom/sessions that no sweep ever enumerates.
func TestMintIdentity_InMemoryStoreTakesNoLivenessLock(t *testing.T) {
	testsupport.Isolate(t)

	// Hostile fixture: the real store's mint DOES write the lock here.
	real, err := sessions.Open(nil)
	require.NoError(t, err)
	rid, err := MintIdentity(real, sessions.Seed{ProjectDir: "/proj"}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { sessionlock.Release(rid.Harp) })
	realLock, err := paths.HarpLockPath(rid.Harp)
	require.NoError(t, err)
	require.FileExists(t, realLock, "the real store's mint must hold its lock, or this test measures nothing")

	id, err := MintIdentity(sessions.NewMemStore(), sessions.Seed{ProjectDir: "/proj"}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { sessionlock.Release(id.Harp) })
	memLock, err := paths.HarpLockPath(id.Harp)
	require.NoError(t, err)
	require.NoFileExists(t, memLock, "an in-memory session must leave no liveness lock on disk")
}
