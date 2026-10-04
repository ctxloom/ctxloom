package coord

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// seedMembers plants one file in each of harp's home/, scratch/, native/ and
// spool/, and returns the session dir.
func seedMembers(t *testing.T, harp string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	for _, m := range []string{paths.SessionEngineHomesDirName, paths.ScratchDirName, paths.NativeDirName, paths.SpoolDirName} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, m), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, m, "f"), []byte("x"), 0o600))
	}
	return dir
}

// Close deletes the disposable members — home/ and scratch/ — of EVERY agent
// in the tree, the owner's and each ended child's, and keeps the rest: native
// history, the spool, everything a resume or a human needs.
func TestClose_DeletesEveryTreeAgentsHomeAndScratch(t *testing.T) {
	resetStrictness(t)
	rootsHome(t)
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} })
	c := newRoot(t, ownerIdentity().Harp, "", sp)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	owner := seedMembers(t, ownerIdentity().Harp)
	child := seedMembers(t, out.Harp)

	awaitDrain(t, c.BeginDrain())
	c.Close()

	for _, dir := range []string{owner, child} {
		assert.NoDirExists(t, filepath.Join(dir, paths.SessionEngineHomesDirName), "home/ is rebuilt on a resume")
		assert.NoDirExists(t, filepath.Join(dir, paths.ScratchDirName), "scratch/ is per-run")
		assert.FileExists(t, filepath.Join(dir, paths.NativeDirName, "f"), "native history is kept")
		assert.FileExists(t, filepath.Join(dir, paths.SpoolDirName, "f"), "the spool is kept")
	}
}

// A run that has not ended — adopted with its runner not yet back — may still
// have an engine using its home: Close leaves its members alone.
func TestClose_LeavesTheMembersOfARunThatHasNotEnded(t *testing.T) {
	resetStrictness(t)
	rootsHome(t)
	gate := make(chan struct{})
	sp := startRunSpawner(t, func() *scriptedChat { return &scriptedChat{Gate: gate} })
	first := newRoot(t, ownerIdentity().Harp, "", sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats) == 1 && len(sp.chats[0].RecordedTexts()) == 1
	}, conformanceWait, 5*time.Millisecond)
	crashCoordinator(first)
	sp.killEngine(0)
	child := seedMembers(t, out.Harp)

	second := newRootWith(t, Options{OwnerHarp: "resumer-harp", RootHarp: ownerIdentity().Harp, Spawner: sp})
	require.Equal(t, StateExecuting, rosterState(second, out.Harp), "precondition: the adopted run is live")
	second.Close()

	assert.FileExists(t, filepath.Join(child, paths.SessionEngineHomesDirName, "f"))
	assert.FileExists(t, filepath.Join(child, paths.ScratchDirName, "f"))
}

// A run whose engine kept its history as a real dir in its home (a container
// run on a host whose links do not resolve in a container) has it moved into
// native/ before Close deletes the home.
func TestClose_MovesAHomesRealHistoryIntoNativeFirst(t *testing.T) {
	resetStrictness(t)
	rootsHome(t)
	c := newRoot(t, ownerIdentity().Harp, "", startRunSpawner(t, func() *scriptedChat { return &scriptedChat{} }))
	dir, err := paths.HarpDir(ownerIdentity().Harp)
	require.NoError(t, err)
	native := filepath.Join(dir, paths.NativeDirName, "claude", "projects")
	require.NoError(t, os.MkdirAll(native, 0o700))
	written := filepath.Join(dir, paths.SessionEngineHomesDirName, "claude", "projects", "-p", "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(written), 0o700))
	require.NoError(t, os.WriteFile(written, []byte("container\n"), 0o600))

	awaitDrain(t, c.BeginDrain())
	c.Close()

	assert.NoDirExists(t, filepath.Join(dir, paths.SessionEngineHomesDirName))
	assert.FileExists(t, filepath.Join(native, "-p", "s.jsonl"), "the history outlives the home")
}
