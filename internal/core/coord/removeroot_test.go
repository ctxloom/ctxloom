package coord

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setSeam installs a test seam and restores the previous one at cleanup.
func setSeam[F any](t *testing.T, seam *F, f F) {
	t.Helper()
	prev := *seam
	*seam = f
	t.Cleanup(func() { *seam = prev })
}

// TestRemoveRoot_RefusesARootALiveProcessHolds: removal CLAIMS the root
// first, so a root a live process owns is refused by name and left whole.
func TestRemoveRoot_RefusesARootALiveProcessHolds(t *testing.T) {
	rootsHome(t)
	live := newRoot(t, "live-owner-harp", "", newFakeSpawner(t, nil, nil))

	err := RemoveRoot(safefs.New(), rootsProjectID, "", "live-owner-harp")

	require.ErrorIs(t, err, ErrStateOwned)
	assert.FileExists(t, filepath.Join(live.StateDir(), "runs.jsonl"))
}

// TestRemoveRoot_RemovesAnUnheldRoot: a root nobody holds is deleted whole;
// one that does not exist is already removed.
func TestRemoveRoot_RemovesAnUnheldRoot(t *testing.T) {
	rootsHome(t)
	dir, err := ensureRootStateDir(afero.NewOsFs(), rootsProjectID, "", "left-harp")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "runs.jsonl"), []byte("{}\n"), 0o600))

	require.NoError(t, RemoveRoot(safefs.New(), rootsProjectID, "", "left-harp"))
	assert.NoDirExists(t, dir)
	require.NoError(t, RemoveRoot(safefs.New(), rootsProjectID, "", "left-harp"), "removing a missing root is not an error")
	assert.NoDirExists(t, dir, "and it does not create one")
}

// TestRoots_AClaimRacingARemovalBeforeItLocksStillStands forces the
// interleaving where a resume (`--session H`) has made root H's dir and
// RemoveRoot then deletes it before the claimant takes the lock. The
// claimant must not fail on the missing dir: it makes the root again and
// stands on it.
func TestRoots_AClaimRacingARemovalBeforeItLocksStillStands(t *testing.T) {
	rootsHome(t)
	dir, err := ensureRootStateDir(afero.NewOsFs(), rootsProjectID, "", "resumed-harp")
	require.NoError(t, err)
	fired := false
	setSeam(t, &beforeRootClaim, func(string) {
		if !fired {
			fired = true
			require.NoError(t, RemoveRoot(safefs.New(), rootsProjectID, "", "resumed-harp"))
			require.NoDirExists(t, dir, "precondition: the root is gone under the claimant")
		}
	})

	c := newRoot(t, "resumer-harp", "resumed-harp", newFakeSpawner(t, nil, nil))

	require.True(t, fired)
	assert.Equal(t, dir, c.StateDir())
	assert.FileExists(t, filepath.Join(dir, "runs.jsonl"), "the claimant stands on a root of its own making")
	st, err := ProbeOwner(safefs.New(), dir)
	require.NoError(t, err)
	assert.True(t, st.Held)
}

// TestRoots_AClaimPollingWhileARemovalHoldsTheLockStillStands forces the
// other interleaving: RemoveRoot holds root H's lock, a resume of H is
// already polling for it, and only then is the dir deleted and the lock
// released. The claimant must end up owning a live root, never a lock on a
// file in a deleted dir.
func TestRoots_AClaimPollingWhileARemovalHoldsTheLockStillStands(t *testing.T) {
	rootsHome(t)
	dir, err := ensureRootStateDir(afero.NewOsFs(), rootsProjectID, "", "resumed-harp")
	require.NoError(t, err)

	contended := make(chan struct{}, 1)
	setSeam(t, &onOwnerLockContended, func(string) {
		select {
		case contended <- struct{}{}:
		default:
		}
	})
	type claim struct {
		c   *Coordinator
		err error
	}
	done := make(chan claim, 1)
	setSeam(t, &whileRemovingRoot, func(string) {
		go func() {
			c, err := New(Options{ProjectDir: t.TempDir(), ProjectID: rootsProjectID, Spawner: newFakeSpawner(t, nil, nil),
				OwnerHarp: "resumer-harp", RootHarp: "resumed-harp", Reporter: termSink()})
			done <- claim{c, err}
		}()
		<-contended // the claimant is polling the lock RemoveRoot holds
	})

	require.NoError(t, RemoveRoot(safefs.New(), rootsProjectID, "", "resumed-harp"))
	got := <-done
	require.NoError(t, got.err, "a claim racing a removal must not fail on the removed dir")
	t.Cleanup(got.c.Close)
	assert.Equal(t, dir, got.c.StateDir())
	assert.FileExists(t, filepath.Join(dir, "runs.jsonl"))
	st, err := ProbeOwner(safefs.New(), dir)
	require.NoError(t, err)
	assert.True(t, st.Held, "the claimant holds a lock on the live root's file")
}

// TestLockOwner_ALockOnAnUnlinkedFileIsNoClaim: a claimant can win the
// lock on a lock FILE that a removal unlinked after the claimant opened it.
// That lock guards nothing anyone else can see, so it is not a claim: the
// claimant is told the root was removed, and retries.
func TestLockOwner_ALockOnAnUnlinkedFileIsNoClaim(t *testing.T) {
	rootsHome(t)
	dir, err := ensureRootStateDir(afero.NewOsFs(), rootsProjectID, "", "replaced-harp")
	require.NoError(t, err)
	lockPath := filepath.Join(dir, OwnerLockFileName)
	setSeam(t, &afterOwnerLock, func(p string) {
		require.NoError(t, os.Remove(p))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	})

	fl, err := lockOwner(safefs.New().Locks, lockPath)

	assert.Nil(t, fl)
	require.ErrorIs(t, err, errRootRemoved)
}
