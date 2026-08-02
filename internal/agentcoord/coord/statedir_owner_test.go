package coord

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claimOwner created owner.pid exclusively and then ignored both the
// pid write's and the close's error, so a failed write left a ZERO-BYTE lock
// behind while the claim was reported as WON. The next claimant reads that file,
// strconv.Atoi("") fails, the liveness probe is skipped entirely, and the lock
// is deleted as stale — two live coordinators then share one project's journals,
// which is the single failure the single-writer discipline can never recover
// from.
//
// The assertion is on the FILESYSTEM (no lock left) and on the claim being
// declined, not on an error string: a claim that "succeeds" while leaving a lock
// nobody can read is the defect.
func TestClaimOwner_UnstampableLockIsRemovedAndTheClaimDeclined(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "owner.pid")

	prev := writeOwnerPID
	writeOwnerPID = func(f *os.File, _ int) error {
		_ = f.Close()
		return assert.AnError
	}
	t.Cleanup(func() { writeOwnerPID = prev })

	release, err := claimOwner(dir)
	require.Error(t, err, "a lock that could not be stamped must never be reported as a won claim")
	assert.Nil(t, release, "no release closure may be handed back for a claim that was declined")

	_, statErr := os.Stat(lock)
	assert.True(t, os.IsNotExist(statErr),
		"the unstampable lock must be removed: left behind, it reads to the next claimant as a DEAD owner and both processes end up writing one journal set (stat err: %v)", statErr)
}

// The other half of the same invariant, pinned so the two stay coherent: an
// owner.pid with no readable pid IS treated as stale and replaced. That is only
// safe because the writer above can no longer produce one for a LIVE owner — a
// zero-byte lock can now only be crash debris.
func TestClaimOwner_UnreadableLeftoverLockIsTreatedAsStale(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "owner.pid")
	require.NoError(t, os.WriteFile(lock, nil, 0o600))

	release, err := claimOwner(dir)
	require.NoError(t, err, "crash debris with no pid in it must not wedge the project onto ephemeral state forever")
	require.NotNil(t, release)
	t.Cleanup(release)

	raw, rerr := os.ReadFile(lock)
	require.NoError(t, rerr)
	assert.NotEmpty(t, raw, "the replacement lock must actually carry this process's pid")
}

// The ordinary path: a clean claim stamps a readable pid and its release
// removes the lock.
func TestClaimOwner_StampsPidAndReleasesLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "owner.pid")

	release, err := claimOwner(dir)
	require.NoError(t, err)
	require.NotNil(t, release)

	raw, rerr := os.ReadFile(lock)
	require.NoError(t, rerr)
	assert.NotEmpty(t, raw)

	release()
	_, statErr := os.Stat(lock)
	assert.True(t, os.IsNotExist(statErr), "release must remove the lock")
}
