package coord

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestNew_RefusesAProjectAnotherLiveOwnerHolds pins the single-coordinator
// rule at the constructor: when another live process holds the project's
// owner lock, New returns ErrStateOwned naming that owner's pid and builds
// NOTHING — no state dir of its own, no journals, and the winner's lock is
// left exactly as it was. The loser used to fall back to an ephemeral
// per-process state dir with a warning, which is a second coordinator on the
// same project by another name.
func TestNew_RefusesAProjectAnotherLiveOwnerHolds(t *testing.T) {
	testsupport.Isolate(t)
	teeHome(t) // the state dir resolves against HOME, so redirect it FIRST
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Park the project's owner lock on a live process that is NOT this one.
	// (claimOwner treats a lock held by THIS pid as stale by design, so the
	// test process cannot hold it against itself.)
	const key = "owned-project"
	dir, err := stateDirForProject(key)
	require.NoError(t, err)
	lock := filepath.Join(dir, OwnerLockFileName)
	stamp := strconv.Itoa(os.Getppid()) + "\n"
	require.NoError(t, os.WriteFile(lock, []byte(stamp), 0o600))

	// A complete Options: the claim is the ONLY thing that can refuse here.
	c, err := New(Options{ProjectDir: t.TempDir(), ProjectKey: key, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp})
	assert.Nil(t, c)
	require.ErrorIs(t, err, ErrStateOwned)
	assert.Contains(t, err.Error(), strconv.Itoa(os.Getppid()), "the refusal names the owner's pid so the operator can find the session")

	raw, rerr := os.ReadFile(lock)
	require.NoError(t, rerr)
	assert.Equal(t, stamp, string(raw), "the loser must not restamp or remove the winner's lock")
	journals, gerr := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	require.NoError(t, gerr)
	assert.Empty(t, journals, "the loser must not open journals in the winner's state dir")
	entries, rerr := os.ReadDir(tmp)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "the loser must not mint a state dir of its own")
}
