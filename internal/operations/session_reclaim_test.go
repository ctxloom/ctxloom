package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// THE DESTRUCTIVE PATH IS NEVER EXERCISED AGAINST THE LIVE ~/.ctxloom. Every
// test here calls testsupport.Isolate, which redirects HOME into the test's
// own temp root, so paths.HomeSessionsDir — and therefore everything this
// sweep can reach — resolves inside a directory the test owns and deletes.
// The real session store on a developer's machine holds other sessions' only
// copies of their state; a sweep rehearsed against it would destroy them.

// srOldEnough is a time comfortably before any cutoff these tests use.
var srOldEnough = time.Now().Add(-90 * 24 * time.Hour)

// srSeedHarp plants a harp directory carrying real bytes and back-dates it, so
// "aged" is a property of the fixture rather than of how long the test ran.
func srSeedHarp(t *testing.T, harp string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "persist"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte("{\"bulk\":true}\n"), 0o644))
	srBackdate(t, dir)
	return dir
}

// srBackdate walks dir and sets every mtime to srOldEnough, making the whole
// tree read as aged.
func srBackdate(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, srOldEnough, srOldEnough)
	}))
}

// srSeedDeadSession makes harp read sessionlock.Dead: its lock file exists and
// nothing holds it — what the kernel leaves once the owner has ended, however
// it ended. Hold-then-Release rather than a hand-written file, so the fixture
// cannot drift from the real on-disk shape.
func srSeedDeadSession(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(lock, srOldEnough, srOldEnough))
}

// srSeedLiveSession makes harp read sessionlock.Alive: this test process holds
// the lock for the rest of the test.
func srSeedLiveSession(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(lock, srOldEnough, srOldEnough))
}

func srCutoff() time.Time { return time.Now().Add(-24 * time.Hour) }

// TestReclaimAgedSessions_WithoutAnAgeBound_ReclaimsNothing is the load-bearing
// one. The age bound is REQUIRED, never defaulted: session records are
// local-only and nothing rebuilds them, so a sweep that invented its own
// bound would silently eat history. Omitting it must be an error that removes
// nothing, not a default that removes something.
func TestReclaimAgedSessions_WithoutAnAgeBound_ReclaimsNothing(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), time.Time{}, true)

	require.ErrorIs(t, err, ErrNoAgeBound)
	assert.Equal(t, 0, res.Reclaimed, "an unbounded request must reclaim nothing")
	assert.Empty(t, res.Candidates, "and must not even consider a candidate")
	assert.DirExists(t, dir, "the session's data is untouched")
}

// TestReclaimAgedSessions_RemovesAgedEndedSession is the sweep doing its job:
// an aged session whose owner is provably gone is what this exists to reclaim.
func TestReclaimAgedSessions_RemovesAgedEndedSession(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), true)

	require.NoError(t, err)
	assert.Equal(t, 1, res.Reclaimed)
	assert.Greater(t, res.Bytes, int64(0), "the reclaimed bytes are reported, not assumed")
	assert.NoDirExists(t, dir, "an aged, provably-ended session's data is reclaimed")
}

// TestReclaimAgedSessions_SkipsRunningSession pins the inversion that would
// destroy live state: a HELD lock means the owner is alive, and a held lock
// must never be read as permission to reclaim.
func TestReclaimAgedSessions_SkipsRunningSession(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "live-quiet-heron")
	srSeedLiveSession(t, "live-quiet-heron")

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), true)

	require.NoError(t, err)
	assert.Equal(t, 0, res.Reclaimed, "a running session is never reclaimed, however aged its files are")
	assert.Equal(t, 1, res.Skipped)
	assert.DirExists(t, dir, "the running session's data is untouched")
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionSkipped, res.Candidates[0].Verdict)
	assert.NotEmpty(t, res.Candidates[0].Reason, "a refusal always says why")
}

// TestReclaimAgedSessions_SkipsSessionWithNoLock pins the other half: a
// session with NO lock file cannot be PROVEN dead, and "cannot determine" is
// never permission. A missing lock must never read as a free one — that single
// inversion turns this sweep into data loss for every pre-lock session.
func TestReclaimAgedSessions_SkipsSessionWithNoLock(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "unproven-quiet-heron")
	// Deliberately no lock file at all.

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), true)

	require.NoError(t, err)
	assert.Equal(t, 0, res.Reclaimed, "an unprovable owner is never reclaimed")
	assert.Equal(t, 1, res.Skipped)
	assert.DirExists(t, dir, "the unprovable session's data is untouched")
}

// TestReclaimAgedSessions_LeavesSessionNewerThanTheBound proves the bound is
// actually applied rather than merely required: a recent session is out of
// scope even though its owner is provably gone.
func TestReclaimAgedSessions_LeavesSessionNewerThanTheBound(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "fresh-quiet-heron")
	srSeedDeadSession(t, "fresh-quiet-heron")
	now := time.Now()
	require.NoError(t, os.Chtimes(filepath.Join(dir, "transcript.jsonl"), now, now))

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), true)

	require.NoError(t, err)
	assert.Equal(t, 0, res.Reclaimed, "newer than the bound is out of scope")
	assert.Equal(t, 1, res.Skipped)
	assert.DirExists(t, dir, "recent session data is untouched")
}

// TestReclaimAgedSessions_ReportsWithoutApplying is the read-only default:
// without apply, a full plan is produced and NOT ONE BYTE moves.
func TestReclaimAgedSessions_ReportsWithoutApplying(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), false)

	require.NoError(t, err)
	assert.False(t, res.Applied)
	assert.Equal(t, 0, res.Reclaimed, "a report removes nothing")
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionReclaimable, res.Candidates[0].Verdict, "but it still says what WOULD go")
	assert.Greater(t, res.Bytes, int64(0), "and what that would free")
	assert.DirExists(t, dir, "every byte is still on disk")
}

// TestReclaimAgedSessions_SparesSessionWhoseWorktreeHoldsUncommittedWork is the
// rule that makes a mistaken liveness answer survivable rather than fatal: a
// dirty worktree is REPORTED, never reclaimed. The session is aged and its
// owner is provably gone — every other signal says take it — and it survives
// solely because a checkout inside it holds work that exists nowhere else.
//
// The assertion reads the WIP file's OWN BYTES, not merely that a directory
// exists: this project has lost work to a force-removed worktree before.
func TestReclaimAgedSessions_SparesSessionWhoseWorktreeHoldsUncommittedWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	testsupport.Isolate(t)
	harp := "wip-quiet-heron"
	dir := srSeedHarp(t, harp)
	repo := srInitRepo(t)
	wtDir := srAddWorktree(t, repo, harp, "wip")
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "in-flight.go"), []byte("// uncommitted\n"), 0o644))
	srBackdate(t, dir)
	srSeedDeadSession(t, harp)

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), true)

	require.NoError(t, err)
	assert.Equal(t, 0, res.Reclaimed, "a session holding uncommitted work is never reclaimed")
	assert.Equal(t, 1, res.Spared)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionSpared, res.Candidates[0].Verdict)
	assert.NotEmpty(t, res.Candidates[0].Reason, "and the report says which worktree held it back")

	assert.DirExists(t, dir, "the whole harp directory survives")
	body, rerr := os.ReadFile(filepath.Join(wtDir, "in-flight.go"))
	require.NoError(t, rerr, "the uncommitted file itself must survive, not just its directory")
	assert.Contains(t, string(body), "uncommitted")
}

// TestReclaimAgedSessions_ReclaimsSessionWhoseWorktreeIsClean is the
// contrasting arm, and it is what stops the test above from passing against an
// implementation that simply never reclaims anything with a worktree: a CLEAN
// checkout is torn down through the worktree leaf's own triage and the session
// then goes.
func TestReclaimAgedSessions_ReclaimsSessionWhoseWorktreeIsClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	testsupport.Isolate(t)
	harp := "clean-quiet-heron"
	dir := srSeedHarp(t, harp)
	repo := srInitRepo(t)
	srAddWorktree(t, repo, harp, "clean")
	srBackdate(t, dir)
	srSeedDeadSession(t, harp)

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), true)

	require.NoError(t, err)
	assert.Equal(t, 1, res.Reclaimed, "a clean worktree is torn down and the session reclaimed")
	assert.NoDirExists(t, dir)
}

// srInitRepo creates a real git repo with one commit, so `git worktree add -b`
// has a HEAD to branch from.
func srInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	srGit(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644))
	srGit(t, dir, "add", "README.md")
	srGit(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

// srAddWorktree plants a real linked worktree at the exact path the reaper's
// candidate finder scans: <harp>/ephemeral/ctxloom-wt-<name>.
func srAddWorktree(t *testing.T, repo, harp, name string) string {
	t.Helper()
	ephemeral, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(ephemeral, 0o755))
	wtDir := filepath.Join(ephemeral, "ctxloom-wt-"+name)
	srGit(t, repo, "worktree", "add", "-q", "-b", "wt-"+harp+"-"+name, wtDir)
	t.Cleanup(func() {
		_ = os.RemoveAll(wtDir)
		_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
	})
	return wtDir
}

func srGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
		"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
