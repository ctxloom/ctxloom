package coord

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

	// Park the project's owner lock on a descriptor that is not the claim's:
	// flock conflicts between two open descriptions even within one process,
	// which is all "another live owner" is to the kernel.
	const key = "owned-project"
	dir, err := stateDirForProject(key)
	require.NoError(t, err)
	holdOwnerLock(t, dir)
	owner := ownerStamp{PID: os.Getppid(), Harp: "the-owner-harp", Mode: OwnerNonInteractive, Started: time.Now().UTC()}
	writeStamp(t, dir, owner)
	stampPath := filepath.Join(dir, ownerStampFileName)
	stamp, err := os.ReadFile(stampPath)
	require.NoError(t, err)

	// A complete Options: the claim is the ONLY thing that can refuse here.
	c, err := New(Options{ProjectDir: t.TempDir(), ProjectID: key, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp})
	assert.Nil(t, c)
	require.ErrorIs(t, err, ErrStateOwned)
	assert.Contains(t, err.Error(), strconv.Itoa(owner.PID), "the refusal names the owner's pid so the operator can find the session")
	assert.Contains(t, err.Error(), owner.Harp, "the refusal names the owner's session")

	raw, rerr := os.ReadFile(stampPath)
	require.NoError(t, rerr)
	assert.Equal(t, string(stamp), string(raw), "the loser must not restamp the winner")
	journals, gerr := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	require.NoError(t, gerr)
	assert.Empty(t, journals, "the loser must not open journals in the winner's state dir")
	entries, rerr := os.ReadDir(tmp)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "the loser must not mint a state dir of its own")
}
