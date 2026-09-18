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

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
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

func srReclaim(t *testing.T, scope ReclaimScope, apply bool) SessionReclaimResult {
	t.Helper()
	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), srCutoff(), scope, apply)
	require.NoError(t, err)
	return res
}

// TestReclaimAgedSessions_WithoutAnAgeBound_ReclaimsNothing is the load-bearing
// one at this layer. The bound is REQUIRED here even though the CLI now
// supplies a configured default: this function is the thing that deletes,
// and it must not be callable in a shape that deletes with no bound stated
// at all. Omitting it must be an error that removes nothing.
func TestReclaimAgedSessions_WithoutAnAgeBound_ReclaimsNothing(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res, err := ReclaimAgedSessions(context.Background(), git.NewExec(), time.Time{}, ReclaimEphemeralAndPersist, true)

	require.ErrorIs(t, err, ErrNoAgeBound)
	assert.Equal(t, 0, res.Reclaimed, "an unbounded request must reclaim nothing")
	assert.Empty(t, res.Candidates, "and must not even consider a candidate")
	srAssertIntact(t, dir, paths.EphemeralDirName, paths.PersistDirName, paths.SegmentsDirName)
}

// TestReclaimAgedSessions_ReapsEphemeralAndKeepsEverythingElse is the sweep
// doing its job, and its default scope: an aged session whose owner is
// provably gone loses its DISPOSABLE store and nothing else. persist/ is
// referenced data (open task rows cite paths in it), segments/ is the
// rotation record, and the sidecar and essence are what make the directory
// a session at all — every one of them survives with its bytes.
func TestReclaimAgedSessions_ReapsEphemeralAndKeepsEverythingElse(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Equal(t, 1, res.Reclaimed)
	assert.Greater(t, res.Bytes, int64(0), "the reclaimed bytes are reported, not assumed")
	srAssertGone(t, dir, paths.EphemeralDirName)
	srAssertIntact(t, dir, paths.PersistDirName, paths.SegmentsDirName,
		paths.SessionSidecarFileName, paths.EssenceFileName)
	assert.DirExists(t, dir, "the session directory itself stays: it is still a session")
}

// TestReclaimAgedSessions_ReapsPersistOnlyUnderTheOptIn pins both arms of the
// persist/ rule in one place: the default scope leaves it, the opt-in scope
// takes it — and even the opt-in leaves segments/ and the session's own
// identity files alone.
func TestReclaimAgedSessions_ReapsPersistOnlyUnderTheOptIn(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	srReclaim(t, ReclaimEphemeral, true)
	srAssertIntact(t, dir, paths.PersistDirName)

	res := srReclaim(t, ReclaimEphemeralAndPersist, true)

	assert.Equal(t, 1, res.Reclaimed)
	srAssertGone(t, dir, paths.PersistDirName)
	srAssertIntact(t, dir, paths.SegmentsDirName, paths.SessionSidecarFileName, paths.EssenceFileName)
}

// TestReclaimAgedSessions_BytesCountOnlyWhatTheScopeTakes pins that the
// reported size is the size of what would GO, not of the whole session: a
// caller deciding whether to pass --yes is owed the number that will
// actually change.
func TestReclaimAgedSessions_BytesCountOnlyWhatTheScopeTakes(t *testing.T) {
	testsupport.Isolate(t)
	srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	ephemeralOnly := srReclaim(t, ReclaimEphemeral, false).Bytes
	withPersist := srReclaim(t, ReclaimEphemeralAndPersist, false).Bytes

	var ephemeralBytes, persistBytes int64
	for rel, body := range srLayout {
		switch strings.SplitN(rel, "/", 2)[0] {
		case paths.EphemeralDirName:
			ephemeralBytes += int64(len(body))
		case paths.PersistDirName:
			persistBytes += int64(len(body))
		}
	}
	assert.Equal(t, ephemeralBytes, ephemeralOnly)
	assert.Equal(t, ephemeralBytes+persistBytes, withPersist)
}

// TestReclaimAgedSessions_KeepMarkerExemptsTheSession: a plain file named
// `keep` at the top of a session directory takes the whole session out of
// the sweep, under every scope. It is the one hand-placed override, for the
// session someone is still grepping for a design nothing else records.
func TestReclaimAgedSessions_KeepMarkerExemptsTheSession(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "kept-quiet-heron")
	srSeedDeadSession(t, "kept-quiet-heron")
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionKeepMarkerFileName), nil, 0o644))
	srBackdate(t, dir)

	res := srReclaim(t, ReclaimEphemeralAndPersist, true)

	assert.Equal(t, 0, res.Reclaimed)
	assert.Equal(t, 1, res.Kept)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionKept, res.Candidates[0].Verdict)
	assert.NotEmpty(t, res.Candidates[0].Reason, "the report names the marker")
	srAssertIntact(t, dir, paths.EphemeralDirName, paths.PersistDirName, paths.SegmentsDirName)
}

// TestReclaimAgedSessions_SkipsRunningSession pins the inversion that would
// destroy live state: a HELD lock means the owner is alive, and a held lock
// must never be read as permission to reclaim.
func TestReclaimAgedSessions_SkipsRunningSession(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "live-quiet-heron")
	srSeedLiveSession(t, "live-quiet-heron")

	res := srReclaim(t, ReclaimEphemeralAndPersist, true)

	assert.Equal(t, 0, res.Reclaimed, "a running session is never reclaimed, however aged its files are")
	assert.Equal(t, 1, res.Skipped)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionSkipped, res.Candidates[0].Verdict)
	assert.NotEmpty(t, res.Candidates[0].Reason, "a refusal always says why")
	srAssertIntact(t, dir, paths.EphemeralDirName, paths.PersistDirName, paths.SegmentsDirName)
}

// TestReclaimAgedSessions_SkipsSessionWithNoLock pins the other half: a
// session with NO lock file cannot be PROVEN dead, and "cannot determine" is
// never permission. A missing lock must never read as a free one — that single
// inversion turns this sweep into data loss for every pre-lock session.
func TestReclaimAgedSessions_SkipsSessionWithNoLock(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "unproven-quiet-heron")
	// Deliberately no lock file at all.

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Equal(t, 0, res.Reclaimed, "an unprovable owner is never reclaimed")
	assert.Equal(t, 1, res.Skipped)
	srAssertIntact(t, dir, paths.EphemeralDirName)
}

// TestReclaimAgedSessions_LeavesSessionNewerThanTheBound proves the bound is
// actually applied rather than merely required: a recent session is out of
// scope even though its owner is provably gone. It is COUNTED, not listed —
// with a defaulted age every `ctxloom clean` runs this sweep, and a line per
// recent session would bury the cache report under the sessions that are
// simply in use.
//
// The recent touch is OUTSIDE ephemeral/ on purpose: the clock is the newest
// mtime anywhere in the session, so activity in persist/ protects the
// disposable store too. That is the conservative reading of "last active".
func TestReclaimAgedSessions_LeavesSessionNewerThanTheBound(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "fresh-quiet-heron")
	srSeedDeadSession(t, "fresh-quiet-heron")
	now := time.Now()
	require.NoError(t, os.Chtimes(filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName), now, now))

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Equal(t, 0, res.Reclaimed, "newer than the bound is out of scope")
	assert.Equal(t, 1, res.Newer, "and is counted")
	assert.Empty(t, res.Candidates, "but not listed")
	srAssertIntact(t, dir, paths.EphemeralDirName)
}

// TestReclaimAgedSessions_ReportsWithoutApplying is the read-only default:
// without apply, a full plan is produced and NOT ONE BYTE moves.
func TestReclaimAgedSessions_ReportsWithoutApplying(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")

	res := srReclaim(t, ReclaimEphemeralAndPersist, false)

	assert.False(t, res.Applied)
	assert.Equal(t, 0, res.Reclaimed, "a report removes nothing")
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionReclaimable, res.Candidates[0].Verdict, "but it still says what WOULD go")
	assert.Greater(t, res.Bytes, int64(0), "and what that would free")
	srAssertIntact(t, dir, paths.EphemeralDirName, paths.PersistDirName, paths.SegmentsDirName)
}

// TestReclaimAgedSessions_IgnoresSessionsWithNothingInScope: a session that
// holds nothing the scope would take is not a candidate at all — not
// "skipped", not listed, counted nowhere. There is nothing to decide about
// it, and a plan that listed it would be listing sessions it cannot free a
// byte from.
func TestReclaimAgedSessions_IgnoresSessionsWithNothingInScope(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "bare-quiet-heron")
	srSeedDeadSession(t, "bare-quiet-heron")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, paths.EphemeralDirName)))
	empty := srSeedHarp(t, "empty-quiet-heron")
	srSeedDeadSession(t, "empty-quiet-heron")
	require.NoError(t, os.RemoveAll(filepath.Join(empty, paths.EphemeralDirName)))
	require.NoError(t, os.Mkdir(filepath.Join(empty, paths.EphemeralDirName), 0o755))
	srBackdate(t, empty)

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Empty(t, res.Candidates, "neither an absent nor an empty ephemeral/ is a candidate")
	assert.Equal(t, 0, res.Newer)
	srAssertIntact(t, dir, paths.PersistDirName, paths.SegmentsDirName)
	assert.DirExists(t, filepath.Join(empty, paths.EphemeralDirName), "an empty store is left as it was")

	withPersist := srReclaim(t, ReclaimEphemeralAndPersist, false)
	assert.Len(t, withPersist.Candidates, 2, "under the opt-in, persist/ alone makes a session a candidate")
}

// TestReclaimAgedSessions_RefusesASymlinkedStore: when ephemeral/ itself is a
// symlink, the session is left alone and reported. Following it would turn
// a sweep of disposable session state into a delete of whatever it points
// at; unlinking it would be touching something that is not the store. The
// target — and the link — must be exactly as they were.
func TestReclaimAgedSessions_RefusesASymlinkedStore(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "linked-quiet-heron")
	srSeedDeadSession(t, "linked-quiet-heron")
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("not yours\n"), 0o644))
	require.NoError(t, os.RemoveAll(filepath.Join(dir, paths.EphemeralDirName)))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, paths.EphemeralDirName)))
	srBackdate(t, dir)

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Equal(t, 0, res.Reclaimed)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionSkipped, res.Candidates[0].Verdict)
	body, err := os.ReadFile(filepath.Join(outside, "precious.txt"))
	require.NoError(t, err, "the symlink's target is never touched")
	assert.Equal(t, "not yours\n", string(body))
	fi, err := os.Lstat(filepath.Join(dir, paths.EphemeralDirName))
	require.NoError(t, err, "and the link itself is left in place")
	assert.NotZero(t, fi.Mode()&os.ModeSymlink)
}

// TestReclaimAgedSessions_DoesNotFollowASymlinkInsideTheStore: a symlink
// DEEPER in ephemeral/ (a worktree that linked out, a hand-placed shortcut)
// is unlinked with the store and never followed — the bytes it points at
// outside the session survive untouched.
func TestReclaimAgedSessions_DoesNotFollowASymlinkInsideTheStore(t *testing.T) {
	testsupport.Isolate(t)
	dir := srSeedHarp(t, "deep-quiet-heron")
	srSeedDeadSession(t, "deep-quiet-heron")
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("not yours\n"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, paths.EphemeralDirName, "shortcut")))
	srBackdate(t, dir)

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Equal(t, 1, res.Reclaimed)
	srAssertGone(t, dir, paths.EphemeralDirName)
	body, err := os.ReadFile(filepath.Join(outside, "precious.txt"))
	require.NoError(t, err, "the symlink's target outside the session is never touched")
	assert.Equal(t, "not yours\n", string(body))
}

// TestReclaimAgedSessions_IgnoresASymlinkedHarpDir: a symlink at the
// sessions root that is NAMED like a harp and points outside the tree is not
// a candidate at all. Following it would delete whatever it points at; the
// target's aged, in-scope store must survive with its bytes.
func TestReclaimAgedSessions_IgnoresASymlinkedHarpDir(t *testing.T) {
	testsupport.Isolate(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))
	outside := t.TempDir()
	victim := filepath.Join(outside, paths.EphemeralDirName, "scratch.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(victim), 0o755))
	require.NoError(t, os.WriteFile(victim, []byte("not yours\n"), 0o644))
	srBackdate(t, outside)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked-quiet-heron")))
	srSeedDeadSession(t, "linked-quiet-heron")

	res := srReclaim(t, ReclaimEphemeralAndPersist, true)

	assert.Empty(t, res.Candidates, "a symlinked harp directory is never considered")
	assert.Equal(t, 0, res.Newer)
	body, err := os.ReadFile(victim)
	require.NoError(t, err, "the target outside the tree is never touched")
	assert.Equal(t, "not yours\n", string(body))
}

// TestReclaimAgedSessions_TouchesNothingBesideTheSessions pins the other
// boundary: files and non-harp directories beside the session directories
// under the sessions root are not candidates and are not touched.
func TestReclaimAgedSessions_TouchesNothingBesideTheSessions(t *testing.T) {
	testsupport.Isolate(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	srSeedHarp(t, "aged-quiet-heron")
	srSeedDeadSession(t, "aged-quiet-heron")
	stray := filepath.Join(root, "not a harp", paths.EphemeralDirName, "scratch.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(stray), 0o755))
	require.NoError(t, os.WriteFile(stray, []byte("stray\n"), 0o644))
	loose := filepath.Join(root, "index.yaml.migrated")
	require.NoError(t, os.WriteFile(loose, []byte("old index\n"), 0o644))
	srBackdate(t, root)

	res := srReclaim(t, ReclaimEphemeralAndPersist, true)

	assert.Equal(t, 1, res.Reclaimed, "only the harp directory is a candidate")
	assert.FileExists(t, stray)
	assert.FileExists(t, loose)
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

	res := srReclaim(t, ReclaimEphemeral, true)

	assert.Equal(t, 0, res.Reclaimed, "a session holding uncommitted work is never reclaimed")
	assert.Equal(t, 1, res.Spared)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, SessionSpared, res.Candidates[0].Verdict)
	assert.NotEmpty(t, res.Candidates[0].Reason, "and the report says which worktree held it back")

	srAssertIntact(t, dir, paths.EphemeralDirName)
	body, rerr := os.ReadFile(filepath.Join(wtDir, "in-flight.go"))
	require.NoError(t, rerr, "the uncommitted file itself must survive, not just its directory")
	assert.Contains(t, string(body), "uncommitted")
}

// TestReclaimAgedSessions_ReclaimsSessionWhoseWorktreeIsClean is the
// contrasting arm, and it is what stops the test above from passing against an
// implementation that simply never reclaims anything with a worktree: a CLEAN
// checkout is torn down through the worktree leaf's own triage and the store
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

	res := srReclaim(t, ReclaimEphemeral, true)

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
		isolation.SessionState{Harp: harp},
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
