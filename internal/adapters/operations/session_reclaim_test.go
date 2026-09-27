package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// THE DESTRUCTIVE PATH IS NEVER EXERCISED AGAINST THE LIVE ~/.ctxloom. Every
// test here calls testsupport.Isolate, which redirects HOME into the test's
// own temp root, so paths.HomeSessionsDir — and therefore everything this
// sweep can reach — resolves inside a directory the test owns and deletes.
// The real session store on a developer's machine holds other sessions' only
// copies of their state; a sweep rehearsed against it would destroy them.
//
// EVERY OUTCOME IS ASSERTED ON THE FILESYSTEM — what exists after, and with
// which bytes — never on the sweep's own report alone. A report that says
// "reclaimed" while the bytes are still there, or "kept" while they are
// gone, is exactly the failure these tests exist to catch.

// srOldEnough is a time comfortably before any cutoff these tests use.
var srOldEnough = time.Now().Add(-90 * 24 * time.Hour)

// srSeedHarp plants a harp directory in the real session-dir layout — the
// sidecar and essence at the top, ephemeral/ and persist/ and segments/ as
// peers, each holding bytes — and back-dates it, so "aged" is a property of
// the fixture rather than of how long the test ran.
//
// The layout is the point: the sweep's whole contract is WHICH of these
// members it takes, so a fixture missing one would let an over-broad delete
// pass unnoticed.
func srSeedHarp(t *testing.T, harp string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	for rel, body := range srLayout {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	srBackdate(t, dir)
	return dir
}

// srLayout is the fixture's member set, relative to the harp directory.
var srLayout = map[string]string{
	paths.SessionSidecarFileName:                                   "project_dir: /tmp/demo\n",
	paths.EssenceFileName:                                          "# essence\n",
	paths.EphemeralDirName + "/scratch.txt":                        "disposable scratch\n",
	paths.EphemeralDirName + "/overlay/settings.json":              "{}\n",
	paths.PersistDirName + "/" + paths.CanonicalTranscriptFileName: "{\"bulk\":true}\n",
	paths.PersistDirName + "/design" + paths.PlanFileExt:           "# a plan someone cites\n",
	paths.SegmentsDirName + "/abc.jsonl":                           "{\"seg\":1}\n",
}

// srAssertIntact asserts that every fixture member under the given
// top-level names still exists with its ORIGINAL bytes — a directory that
// survives with its files gutted is not "intact".
func srAssertIntact(t *testing.T, dir string, members ...string) {
	t.Helper()
	for rel, body := range srLayout {
		top := strings.SplitN(rel, "/", 2)[0]
		for _, m := range members {
			if top != m {
				continue
			}
			got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			require.NoError(t, err, "%s must survive", rel)
			assert.Equal(t, body, string(got), "%s must survive with its bytes", rel)
		}
	}
}

// srAssertGone asserts that the named top-level members no longer exist.
func srAssertGone(t *testing.T, dir string, members ...string) {
	t.Helper()
	for _, m := range members {
		assert.NoFileExists(t, filepath.Join(dir, m), "%s must be reclaimed", m)
		assert.NoDirExists(t, filepath.Join(dir, m), "%s must be reclaimed", m)
	}
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

func srCutoff() time.Time { return time.Now().Add(-24 * time.Hour) }

// srReclaim is `clean`'s session half: the sweep with only its reclaim rows,
// over every project.
func srReclaim(t *testing.T, p sessions.ReapPolicy) sessions.Report {
	t.Helper()
	res, err := SweepSessions(context.Background(), git.NewExec(), srRequest(p))
	require.NoError(t, err)
	require.NotNil(t, res.Reclaim)
	return *res.Reclaim
}

func srRequest(p sessions.ReapPolicy) SweepRequest {
	return SweepRequest{ReclaimCutoff: p.Cutoff, ReclaimScope: p.Scope, Apply: p.Apply, AllProjects: true, ReclaimOnly: true}
}

// TestSweepReclaim_WithoutAnAgeBound_ReclaimsNothing: the reaper's
// refusal passes through this adapter untouched — the CLI's defaulted age
// is the CLI's, and this layer must not supply one.
func TestSweepReclaim_WithoutAnAgeBound_ReclaimsNothing(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	_, err := SweepSessions(context.Background(), git.NewExec(), srRequest(sessions.ReapPolicy{Apply: true}))

	require.ErrorIs(t, err, sessions.ErrNoAgeBound)
	srAssertIntact(t, dir, paths.EphemeralDirName, paths.PersistDirName, paths.SegmentsDirName)
}

// TestSweepReclaim_TakesThePolicysMembers: the adapter removes what
// the policy names and nothing else — the table's Ephemeral rows by default,
// persist/ besides under Scope Persist. The reaper's own contract is pinned
// in core (sessions.Reap's tests); this is the adapter's end of it.
func TestSweepReclaim_TakesThePolicysMembers(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Reclaimed)
	assert.Equal(t, sessions.ReapPolicy{}.MemberRels(), res.Members)
	srAssertGone(t, dir, paths.EphemeralDirName)
	srAssertIntact(t, dir, paths.PersistDirName, paths.SegmentsDirName)

	res = srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Scope: paths.Persist, Apply: true})

	assert.Equal(t, 1, res.Reclaimed)
	srAssertGone(t, dir, paths.PersistDirName)
	srAssertIntact(t, dir, paths.SegmentsDirName)
}

// TestSweepReclaim_SkipsARunningSession pins the lock adapter's one
// permitting verdict from the held side: a lock this process holds is a
// running owner, and the reap skips the session naming its pid.
func TestSweepReclaim_SkipsARunningSession(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	require.NoError(t, sessionlock.Hold("aged-quiet-heron"))
	t.Cleanup(func() { sessionlock.Release("aged-quiet-heron") })
	srBackdate(t, dir)

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Skipped)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, sessions.ReapSkipped, res.Candidates[0].Verdict)
	assert.Equal(t, os.Getpid(), res.Candidates[0].OwnerPID)
	srAssertIntact(t, dir, paths.EphemeralDirName)
}

// TestSweepReclaim_SkipsASessionWithNoLock pins the adapter from the
// missing side: no lock file at all — a session from before the lock
// existed, or whose Hold failed — cannot be proven dead, and "cannot
// determine" is never permission.
func TestSweepReclaim_SkipsASessionWithNoLock(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Skipped)
	require.Len(t, res.Candidates, 1)
	assert.Contains(t, res.Candidates[0].Reason, "no session lock")
	srAssertIntact(t, dir, paths.EphemeralDirName)
}

// TestSweepReclaim_LeavesTheLockFileBehind: unlinking the lock while
// holding it would let a session resuming under this harp lock a FRESH inode
// and believe it owns the session being deleted.
func TestSweepReclaim_LeavesTheLockFileBehind(t *testing.T) {
	testsupport.Isolate(t)
	srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Reclaimed)
	lock, err := paths.HarpLockPath("aged-quiet-heron")
	require.NoError(t, err)
	assert.FileExists(t, lock)
}

// TestSweepReclaim_SparesSessionWhoseWorktreeHoldsUncommittedWork is the
// rule that makes a mistaken liveness answer survivable rather than fatal: a
// dirty worktree is REPORTED, never reclaimed. The session is aged and its
// owner is provably gone — every other signal says take it — and it survives
// solely because a checkout inside it holds work that exists nowhere else.
//
// The assertion reads the WIP file's OWN BYTES, not merely that a directory
// exists: this project has lost work to a force-removed worktree before.
func TestSweepReclaim_SparesSessionWhoseWorktreeHoldsUncommittedWork(t *testing.T) {
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

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 0, res.Reclaimed, "a session holding uncommitted work is never reclaimed")
	assert.Equal(t, 1, res.Spared)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, sessions.ReapSpared, res.Candidates[0].Verdict)
	assert.NotEmpty(t, res.Candidates[0].Reason, "and the report says which worktree held it back")

	srAssertIntact(t, dir, paths.EphemeralDirName)
	body, rerr := os.ReadFile(filepath.Join(wtDir, "in-flight.go"))
	require.NoError(t, rerr, "the uncommitted file itself must survive, not just its directory")
	assert.Contains(t, string(body), "uncommitted")
}

// TestSweepReclaim_ReclaimsSessionWhoseWorktreeIsClean is the
// contrasting arm, and it is what stops the test above from passing against an
// implementation that simply never reclaims anything with a worktree: a CLEAN
// checkout is torn down through the worktree leaf's own triage and the store
// then goes.
func TestSweepReclaim_ReclaimsSessionWhoseWorktreeIsClean(t *testing.T) {
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

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Reclaimed, "a clean worktree is torn down and the store reclaimed")
	srAssertGone(t, dir, paths.EphemeralDirName)
	srAssertIntact(t, dir, paths.PersistDirName)
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

// srAddWorktree plants a real scratch worktree for harp THROUGH PRODUCTION
// CODE — isolation.Prepare on the worktree workspace axis — rather than by
// hand-rolling `git worktree add`.
//
// That is deliberate on two counts. The checkout has to land at exactly the
// path isolation.findEphemeralWorktrees scans (<harp>/ephemeral/ctxloom-wt-*)
// or this sweep never sees it and the test asserts nothing; and a fixture that
// built that layout by hand would be a second definition of it, free to drift
// from the one production writes. Asking production to build it means the two
// cannot disagree. It is also why this file needs no entry in
// taskstest.sanctionedWorktreeFixtureFiles: it constructs no worktree body of
// its own.
//
// THE DEGRADE GUARD IS LOAD-BEARING. isolation.Prepare walks a degrade chain
// and never fails: if the worktree policy cannot prepare, it silently falls
// back to the project directory. The returned workspace would then be the repo
// itself, nothing would exist under the harp's ephemeral dir, and every
// assertion here would pass while measuring nothing. So the workspace's own
// directory is checked to be under that ephemeral dir before any test uses it.
func srAddWorktree(t *testing.T, repo, harp, agentID string) string {
	t.Helper()
	_, ws := isolation.Prepare(
		context.Background(),
		isolation.Axes{Workspace: isolation.WorkspaceWorktree, Runtime: isolation.RuntimeHost},
		"", isolation.ImageConfig{}, repo, agentID,
		isolation.SessionState{Harp: harp}, nil,
	)
	require.NotNil(t, ws)
	wtDir := ws.Dir()

	ephemeral, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(wtDir, ephemeral+string(filepath.Separator)),
		"isolation.Prepare degraded away from the worktree axis: the workspace is %q, which is not under %q. "+
			"This fixture would then plant nothing the sweep can see, and every assertion built on it would "+
			"pass while measuring nothing.", wtDir, ephemeral)

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
