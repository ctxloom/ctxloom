package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
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
		id, err := MintIdentity(store, tc.seed, compositetest.Trust(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { sessionlock.Release(id.Harp) })
		got, err := store.Find(id.Harp)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got.Origin)
	}
}

// session.yaml records whether the run that minted the session waived
// signature verification — read off the generation's Trust, the same value
// that decided what the session was delivered, never a caller's separate bool.
func TestMintIdentity_StampsTheTrustsSignatureCheckPosture(t *testing.T) {
	testsupport.Isolate(t)
	root, records, retraction := compositetest.Ports()
	waived, err := composite.NewTrust(root, records, retraction, composite.WithoutSignatureCheck())
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		tr   composite.Trust
		want bool
	}{
		{"enforced", compositetest.Trust(), false},
		{"waived", waived, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := sessions.NewMemStore()
			id, err := MintIdentity(store, sessions.Seed{ProjectDir: "/proj"}, tc.tr, t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { sessionlock.Release(id.Harp) })
			got, err := store.Find(id.Harp)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.SigCheckDisabled)
		})
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
	rid, err := MintIdentity(real, sessions.Seed{ProjectDir: "/proj"}, compositetest.Trust(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { sessionlock.Release(rid.Harp) })
	realLock, err := paths.HarpLockPath(rid.Harp)
	require.NoError(t, err)
	require.FileExists(t, realLock, "the real store's mint must hold its lock, or this test measures nothing")

	id, err := MintIdentity(sessions.NewMemStore(), sessions.Seed{ProjectDir: "/proj"}, compositetest.Trust(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { sessionlock.Release(id.Harp) })
	memLock, err := paths.HarpLockPath(id.Harp)
	require.NoError(t, err)
	require.NoFileExists(t, memLock, "an in-memory session must leave no liveness lock on disk")
}
