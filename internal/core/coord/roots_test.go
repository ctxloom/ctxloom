package coord

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// rootsProjectID is the project every root in these tests belongs to: the
// roots differ by harp alone, which is the whole point.
const rootsProjectID = "roots-project"

// newRoot stands a coordinator up, serving, on its CLAIMED root dir (no
// StateDir override): ownerHarp is the session that owns this process,
// rootHarp the root it founds or adopts ("" founds its own).
func newRoot(t *testing.T, ownerHarp, rootHarp string, sp Spawner) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectID:  rootsProjectID,
		Spawner:    sp,
		OwnerHarp:  ownerHarp,
		RootHarp:   rootHarp,
		Reporter:   termSink(),
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)
	return c
}

// rootsHome isolates the process and points HOME at a fresh dir: every root
// dir resolves under it.
func rootsHome(t *testing.T) {
	t.Helper()
	testsupport.Isolate(t)
	teeHome(t)
}

// TestRoots_TwoFreshRunsInOneProjectAreIndependentTrees: two session-owning
// processes in ONE project both get a coordinator, each on a root dir of its
// own under the project, each holding its own owner lock. Both stand at the
// same time — the second is not refused, and neither waits for the other.
func TestRoots_TwoFreshRunsInOneProjectAreIndependentTrees(t *testing.T) {
	rootsHome(t)
	a := newRoot(t, "tree-a-harp", "", newFakeSpawner(nil, nil))
	b := newRoot(t, "tree-b-harp", "", newFakeSpawner(nil, nil))

	assert.NotEqual(t, a.StateDir(), b.StateDir(), "each tree owns a root dir of its own")
	assert.Equal(t, filepath.Dir(a.StateDir()), filepath.Dir(b.StateDir()), "both roots live under the one project")
	wantA, err := RootStateDir(rootsProjectID, "", "tree-a-harp")
	require.NoError(t, err)
	assert.Equal(t, wantA, a.StateDir(), "a fresh run founds the root named by its own harp")

	for _, c := range []*Coordinator{a, b} {
		st, err := ProbeOwner(c.StateDir())
		require.NoError(t, err)
		assert.True(t, st.Held, "both roots are owned at once: %s", c.StateDir())
	}
}

// TestRoots_ResumeAdoptsTheNamedRootAndAFreshRunLeavesItAlone: a root whose
// owner died with a run still live is adopted by the process that resumes it
// (RootHarp names it) — the run is in its roster, held open for its runner's
// re-Hello — while a FRESH run in the same project founds its own root and
// neither sees nor writes the abandoned one.
func TestRoots_ResumeAdoptsTheNamedRootAndAFreshRunLeavesItAlone(t *testing.T) {
	resetStrictness(t)
	rootsHome(t)
	gate := make(chan struct{}) // never closed: the run is mid-turn when its owner dies
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })

	first := newRoot(t, ownerIdentity().Harp, "", sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats) == 1 && len(sp.chats[0].RecordedTexts()) == 1
	}, conformanceWait, 5*time.Millisecond, "the turn must reach the engine before the owner dies")
	rootH := first.StateDir()
	crashCoordinator(first)
	sp.killEngine(0)
	journal, err := os.ReadFile(filepath.Join(rootH, "runs.jsonl"))
	require.NoError(t, err)

	fresh := newRoot(t, "fresh-run-harp", "", sp)
	assert.NotEqual(t, rootH, fresh.StateDir())
	assert.Equal(t, "", rosterState(fresh, out.Harp), "a fresh run does not adopt another root's runs")
	fresh.Close()
	after, err := os.ReadFile(filepath.Join(rootH, "runs.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, journal, after, "a fresh run must not write the abandoned root's journal")

	resumer := newRoot(t, "resumer-harp", ownerIdentity().Harp, sp)
	assert.Equal(t, rootH, resumer.StateDir(), "resuming names the root to adopt")
	assert.Equal(t, StateExecuting, rosterState(resumer, out.Harp), "the resumed root's live run is adopted and held for its runner")
}

// TestRoots_ASecondClaimOnOneRootIsRefused: ErrStateOwned now means one
// thing — THIS root is already adopted by a live process. Resuming a root
// whose owner still runs is refused by name and builds nothing.
func TestRoots_ASecondClaimOnOneRootIsRefused(t *testing.T) {
	rootsHome(t)
	live := newRoot(t, "live-owner-harp", "", newFakeSpawner(nil, nil))

	c, err := New(Options{ProjectDir: t.TempDir(), ProjectID: rootsProjectID, Spawner: newFakeSpawner(nil, nil),
		OwnerHarp: "resumer-harp", RootHarp: "live-owner-harp", Reporter: termSink()})
	assert.Nil(t, c)
	require.ErrorIs(t, err, ErrStateOwned)
	assert.Contains(t, err.Error(), "live-owner-harp", "the refusal names the root's live owner")
	st, err := ProbeOwner(live.StateDir())
	require.NoError(t, err)
	assert.Equal(t, "live-owner-harp", st.Harp, "the refused claim must not restamp the live owner")
}

// TestRoots_CloseRemovesASettledRoot: a root whose every run has ended (here:
// none ever started) is garbage the moment its owner closes — nothing can be
// adopted from it — so Close deletes it, and it no longer lists.
func TestRoots_CloseRemovesASettledRoot(t *testing.T) {
	rootsHome(t)
	c := newRoot(t, "one-shot-harp", "", newFakeSpawner(nil, nil))
	dir := c.StateDir()
	require.DirExists(t, dir)

	c.Close()

	assert.NoDirExists(t, dir, "a settled root is removed by its owner's Close")
	roots, err := ListRoots(rootsProjectID, "")
	require.NoError(t, err)
	assert.Empty(t, roots)
}

// TestRoots_CloseKeepsARootWithALiveRun: a run that has not ended — adopted
// with its runner not yet back, so the drain leaves it — is still adoptable
// by whoever resumes the root, so Close must leave the root in place.
func TestRoots_CloseKeepsARootWithALiveRun(t *testing.T) {
	resetStrictness(t)
	rootsHome(t)
	gate := make(chan struct{})
	sp := startRunSpawner(func() *scriptedChat { return &scriptedChat{Gate: gate} })

	first := newRoot(t, ownerIdentity().Harp, "", sp)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		return len(sp.chats) == 1 && len(sp.chats[0].RecordedTexts()) == 1
	}, conformanceWait, 5*time.Millisecond)
	dir := first.StateDir()
	crashCoordinator(first)
	sp.killEngine(0)

	second := newRoot(t, "resumer-harp", ownerIdentity().Harp, sp)
	require.Equal(t, StateExecuting, rosterState(second, out.Harp), "precondition: the adopted run is live")
	second.Close()

	assert.DirExists(t, dir, "a root holding a run that has not ended survives its owner's Close")
	assert.FileExists(t, filepath.Join(dir, "runs.jsonl"))
}

// TestRoots_CloseLeavesAnExplicitStateDir: Options.StateDir is the caller's
// directory, not a claimed root, so Close never deletes it.
func TestRoots_CloseLeavesAnExplicitStateDir(t *testing.T) {
	teeHome(t)
	dir := t.TempDir()
	c, err := New(Options{ProjectDir: dir, StateDir: dir, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp, Reporter: termSink()})
	require.NoError(t, err)
	c.Close()
	assert.FileExists(t, filepath.Join(dir, "runs.jsonl"))
}

// TestListRoots_ReportsLiveAndOrphanedRoots: every root under the project is
// listed with its owner as a probe sees it — a live owner, a provably
// abandoned interactive owner, and a root nobody holds — and nothing that is
// not a claimed root (a dir never locked, a stray file) is listed.
func TestListRoots_ReportsLiveAndOrphanedRoots(t *testing.T) {
	rootsHome(t)
	mkRoot := func(harp string) string {
		dir, err := RootStateDir(rootsProjectID, "", harp)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, OwnerLockFileName), nil, 0o600))
		return dir
	}
	live := mkRoot("live-harp")
	holdOwnerLock(t, live)
	writeStamp(t, live, ownerStamp{PID: os.Getpid(), Harp: "live-harp", Mode: OwnerNonInteractive, Started: time.Now().UTC()})

	orphan := mkRoot("orphan-harp")
	holdOwnerLock(t, orphan)
	writeStamp(t, orphan, ownerStamp{PID: os.Getpid(), Harp: "orphan-harp", Mode: OwnerInteractive, Started: time.Now().UTC(), StartTicks: orphanTicks})
	fakeProc(t, procpin.Stat{PPID: 1, TTYNr: 0, StartTicks: orphanTicks}, nil)

	mkRoot("unheld-harp")

	notRoot, err := RootStateDir(rootsProjectID, "", "artifacts")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(notRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(notRoot), "stray.json"), nil, 0o600))

	roots, err := ListRoots(rootsProjectID, "")
	require.NoError(t, err)
	byHarp := map[string]RootStatus{}
	for _, r := range roots {
		byHarp[r.RootHarp] = r
	}
	require.Len(t, byHarp, 3, "exactly the three claimed roots list: %+v", roots)

	assert.True(t, byHarp["live-harp"].Owner.Held)
	assert.False(t, byHarp["live-harp"].Owner.Orphan)
	assert.Equal(t, live, byHarp["live-harp"].Dir)

	assert.True(t, byHarp["orphan-harp"].Owner.Held)
	assert.True(t, byHarp["orphan-harp"].Owner.Orphan, "an abandoned interactive owner is reported as such")

	assert.False(t, byHarp["unheld-harp"].Owner.Held, "a root nobody holds is listed, unowned")
}

// TestListRoots_NoProjectStateIsNoRoots: a project no coordinator ever stood
// up in has no roots, and that is not an error.
func TestListRoots_NoProjectStateIsNoRoots(t *testing.T) {
	rootsHome(t)
	roots, err := ListRoots("never-hosted", "")
	require.NoError(t, err)
	assert.Empty(t, roots)
}

// TestRootStateDir_RefusesAnUnusableRootHarp: the root harp becomes one path
// segment, so anything that is not one is refused before any path is built.
func TestRootStateDir_RefusesAnUnusableRootHarp(t *testing.T) {
	rootsHome(t)
	for _, h := range []string{"", ".", "..", "a/b", `a\b`} {
		_, err := RootStateDir(rootsProjectID, "", h)
		assert.Error(t, err, "root harp %q", h)
	}
}
