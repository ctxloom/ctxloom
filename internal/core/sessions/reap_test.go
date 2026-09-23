package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// THE DESTRUCTIVE PATH IS NEVER EXERCISED AGAINST THE LIVE ~/.ctxloom. Every
// test here isolates HOME first (testsupport.Isolate), so the Layout it
// reaps resolves inside a directory the test owns. The real sessions store
// on a developer's machine holds live sessions — including the one running
// this test's author.
//
// EVERY OUTCOME IS ASSERTED ON THE FILESYSTEM — what exists after, and with
// which bytes — never on the report alone. A report that says "reclaimed"
// while the bytes are still there is exactly the failure these tests exist
// to catch.

// reapOldEnough is a time comfortably before any cutoff these tests use.
var reapOldEnough = time.Now().Add(-90 * 24 * time.Hour)

// reapFixture is one session's members, relative to its dir. Every
// paths.HarpMembers row is represented (TestReap_FixtureCoversTheTable
// asserts it), because the reaper's whole contract is WHICH rows it takes:
// a fixture missing one would let an over-broad delete pass unnoticed. The
// keep marker is the one row deliberately absent — it exempts the session,
// and has its own test.
var reapFixture = map[string]string{
	paths.SessionSidecarFileName:                                                "project_dir: /tmp/demo\n",
	paths.EssenceFileName:                                                       "# essence\n",
	paths.NextStepFileName:                                                      "finish the reaper\n",
	paths.SessionEngineHomesDirName + "/settings.json":                          "{}\n",
	paths.SessionEngineHomesDirName + "/.credentials.json":                      "{\"token\":\"copied\"}\n",
	paths.EphemeralDirName + "/scratch.txt":                                     "disposable scratch\n",
	paths.EphemeralDirName + "/overlay/settings.json":                           "{}\n",
	paths.PersistDirName + "/" + paths.CanonicalTranscriptFileName:              "{\"bulk\":true}\n",
	paths.PersistDirName + "/design" + paths.PlanFileExt:                        "# a plan someone cites\n",
	paths.PersistDirName + "/" + paths.TranscriptStoreDirName + "/claude.jsonl": "{\"vendor\":true}\n",
	paths.PersistDirName + "/" + paths.SpoolDirName + "/0001.msg":               "mail\n",
	paths.PersistDirName + "/" + paths.PackageDirName + "/abc123/manifest.yaml": "name: pkg\n",
	paths.SegmentsDirName + "/abc.jsonl":                                        "{\"seg\":1}\n",
}

// reapLinkName is a top-level symlink beside the members — the shape of an
// engine transcript link, which no table row names.
const reapLinkName = paths.EngineTranscriptLinkPrefix + "claude-abc.jsonl"

func reapLayout(t *testing.T) Layout {
	t.Helper()
	testsupport.Isolate(t)
	l, err := HomeLayout()
	require.NoError(t, err)
	return l
}

// fakeLocks is a Locks whose verdict is set per harp: dead unless refused.
// It records, at release time, what the caller had done under the hold, so
// a test can prove the lock was held ACROSS the removal and not merely
// probed before it.
type fakeLocks struct {
	refused   map[string]string
	pid       int
	onRelease func(harp string)
}

func deadLocks() *fakeLocks { return &fakeLocks{refused: map[string]string{}, pid: 4242} }

func (f *fakeLocks) Acquire(harp string) (LockProbe, func()) {
	if why, ok := f.refused[harp]; ok {
		return LockProbe{PID: f.pid, Reason: why}, func() {}
	}
	return LockProbe{Dead: true, PID: f.pid, Reason: "the session's lock was free"}, func() {
		if f.onRelease != nil {
			f.onRelease(harp)
		}
	}
}

// reapSeed plants the fixture for harp under the layout, with a top-level
// symlink, and back-dates every mtime so "aged" is a property of the fixture
// rather than of how long the test ran. Liveness is the Locks port's answer
// (deadLocks unless a test says otherwise).
func reapSeed(t *testing.T, l Layout, harp string) string {
	t.Helper()
	dir := l.Dir(harp)
	for rel, body := range reapFixture {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere.jsonl"), filepath.Join(dir, reapLinkName)))
	reapBackdate(t, dir)
	return dir
}

// reapBackdate sets every mtime under dir to reapOldEnough. Symlinks are
// skipped: os.Chtimes follows them, and their targets do not exist.
func reapBackdate(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(p, reapOldEnough, reapOldEnough)
	}))
}

func reapCutoff() time.Time { return time.Now().Add(-24 * time.Hour) }

// reapAssertPresence asserts, for every fixture member, that it is gone when
// gone(rel) says so and otherwise survives WITH ITS BYTES — a directory that
// survives with its files gutted is not "intact". The top-level symlink is
// asserted present in every case: no scope names it.
func reapAssertPresence(t *testing.T, dir string, gone func(rel string, m paths.HarpMember) bool) {
	t.Helper()
	for rel, body := range reapFixture {
		m, ok := paths.ClassifyMember(rel)
		require.True(t, ok, "fixture member %s classifies to no row", rel)
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if gone(rel, m) {
			assert.NoFileExists(t, p, "%s must be reaped", rel)
			continue
		}
		got, err := os.ReadFile(p)
		if assert.NoError(t, err, "%s must survive", rel) {
			assert.Equal(t, body, string(got), "%s must survive with its bytes", rel)
		}
	}
	_, err := os.Lstat(filepath.Join(dir, reapLinkName))
	assert.NoError(t, err, "the top-level symlink is no member and must be left in place")
	assert.FileExists(t, filepath.Join(dir, paths.SessionSidecarFileName), "the session must still resolve after a reap")
}

// TestReap_FixtureCoversTheTable: every table row is represented by the
// fixture (the keep marker excepted), so a row added to paths.HarpMembers
// forces the fixture — and therefore the reaper's contract — to say what
// happens to it.
func TestReap_FixtureCoversTheTable(t *testing.T) {
	covered := map[string]bool{}
	for rel := range reapFixture {
		m, ok := paths.ClassifyMember(rel)
		require.True(t, ok, "fixture member %s classifies to no row", rel)
		covered[m.Rel()] = true
	}
	for _, m := range paths.HarpMembers {
		if m.Name == paths.SessionKeepMarkerFileName {
			continue
		}
		assert.True(t, covered[m.Rel()], "table row %s has no fixture member; add one so the reaper's contract names it", m.Rel())
	}
}

// TestReap_RemovesExactlyTheEphemeralMembers is the gate: over a fixture
// holding every member, a reap under the default scope removes exactly the
// rows whose Lifetime is Ephemeral — the table decides, nothing else — and
// every Persist row survives with its bytes.
func TestReap_RemovesExactlyTheEphemeralMembers(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Reclaimed)
	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, ReapReclaimed, rep.Candidates[0].Verdict)
	reapAssertPresence(t, dir, func(_ string, m paths.HarpMember) bool { return m.Lifetime == paths.Ephemeral })

	var ephemeralRels []string
	for _, m := range paths.HarpMembers {
		if m.Lifetime == paths.Ephemeral {
			ephemeralRels = append(ephemeralRels, m.Rel())
		}
	}
	assert.Equal(t, ephemeralRels, rep.Members, "the report names the rows the policy takes, in table order")
}

// TestReap_PersistScope_TakesThePersistStoreWithItsTranscript: Scope Persist
// is a human's --include-persist, and from a DISTILLED session it TAKES the
// transcripts with the rest of persist/. The session's identity,
// its essence, its next step and its segments still survive: the directory
// stays, and the session still lists and resolves.
func TestReap_PersistScope_TakesThePersistStoreWithItsTranscript(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Scope: paths.Persist, Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Reclaimed)
	reapAssertPresence(t, dir, func(rel string, m paths.HarpMember) bool {
		return m.Lifetime == paths.Ephemeral || rel == paths.PersistDirName || strings.HasPrefix(rel, paths.PersistDirName+"/")
	})
	assert.NoFileExists(t, filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName), "--include-persist takes the transcript")
	assert.NoDirExists(t, filepath.Join(dir, paths.PersistDirName), "the persist store goes whole")
	assert.Contains(t, rep.Members, paths.PersistDirName)
}

// reapSeedUndistilled is reapSeed without the essence: the session was never
// distilled, so its transcript is the only record of it.
func reapSeedUndistilled(t *testing.T, l Layout, harp string) string {
	t.Helper()
	dir := reapSeed(t, l, harp)
	require.NoError(t, os.Remove(filepath.Join(dir, paths.EssenceFileName)))
	reapBackdate(t, dir)
	return dir
}

// TestReap_PersistScope_SparesThePersistStoreOfAnUndistilledSession: without
// an essence the transcript is the session's only record, so even
// --include-persist leaves persist/ alone — the same rule PurgeSession
// enforces with ErrPurgeUndistilled. The ephemeral members still go: a wider
// scope must never free less than the default one. The report names the
// spare and the command that lifts it.
func TestReap_PersistScope_SparesThePersistStoreOfAnUndistilledSession(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeedUndistilled(t, l, "aged-quiet-heron")

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Scope: paths.Persist, Apply: true}, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName), "an undistilled session's transcript is its only record")
	for rel, body := range reapFixture {
		m, ok := paths.ClassifyMember(rel)
		require.True(t, ok)
		p := filepath.Join(dir, filepath.FromSlash(rel))
		switch {
		case rel == paths.EssenceFileName:
			continue
		case m.Lifetime == paths.Ephemeral:
			assert.NoFileExists(t, p, "%s is ephemeral and is still reaped", rel)
		default:
			got, err := os.ReadFile(p)
			if assert.NoError(t, err, "%s must survive", rel) {
				assert.Equal(t, body, string(got))
			}
		}
	}
	require.Len(t, rep.Candidates, 1)
	c := rep.Candidates[0]
	assert.Equal(t, ReapReclaimed, c.Verdict, "its ephemeral members were reclaimed")
	assert.Contains(t, c.Reason, "never distilled")
	assert.Contains(t, c.Reason, "ctxloom session distill aged-quiet-heron")
}

// TestReap_PersistScope_UndistilledWithOnlyPersistDataIsSpared: when
// persist/ is all an undistilled session holds, nothing is taken and the
// session is reported spared rather than hidden.
func TestReap_PersistScope_UndistilledWithOnlyPersistDataIsSpared(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeedUndistilled(t, l, "aged-quiet-heron")
	for _, m := range (ReapPolicy{}).Members() {
		require.NoError(t, os.RemoveAll(l.Member("aged-quiet-heron", m)))
	}
	reapBackdate(t, dir)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Scope: paths.Persist, Apply: true}, nil)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName))
	assert.Equal(t, 1, rep.Spared)
	assert.Zero(t, rep.Bytes)
	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, ReapSpared, rep.Candidates[0].Verdict)
	assert.Contains(t, rep.Candidates[0].Reason, "never distilled")
	assert.Contains(t, rep.Candidates[0].Reason, "ctxloom session distill aged-quiet-heron")
}

// TestReap_WithoutAnAgeBound_ReapsNothing is the load-bearing one at this
// layer: this function is the thing that deletes, and it must not be callable
// in a shape that deletes with no bound stated at all.
func TestReap_WithoutAnAgeBound_ReapsNothing(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")

	_, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Apply: true}, nil)

	require.ErrorIs(t, err, ErrNoAgeBound)
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_ReportsWithoutApplying: Apply false is report-first — the same
// verdicts, the same bytes, and NOT ONE BYTE moves.
func TestReap_ReportsWithoutApplying(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff()}, nil)
	require.NoError(t, err)

	assert.False(t, rep.Applied)
	assert.Equal(t, 0, rep.Reclaimed)
	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, ReapReclaimable, rep.Candidates[0].Verdict)
	assert.Positive(t, rep.Bytes, "the plan says how much would go")
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_BytesCountOnlyWhatThePolicyTakes: the byte figure is the size of
// what would GO under the policy, not of the session — persist/ counts only
// under Scope Persist.
func TestReap_BytesCountOnlyWhatThePolicyTakes(t *testing.T) {
	l := reapLayout(t)
	reapSeed(t, l, "aged-quiet-heron")

	ephemeral, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff()}, nil)
	require.NoError(t, err)
	persist, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Scope: paths.Persist}, nil)
	require.NoError(t, err)

	var wantEphemeral, wantPersist int64
	for rel, body := range reapFixture {
		m, _ := paths.ClassifyMember(rel)
		if m.Lifetime == paths.Ephemeral {
			wantEphemeral += int64(len(body))
		}
		if m.Lifetime == paths.Ephemeral || strings.HasPrefix(rel, paths.PersistDirName+"/") {
			wantPersist += int64(len(body))
		}
	}
	assert.Equal(t, wantEphemeral, ephemeral.Bytes)
	assert.Equal(t, wantPersist, persist.Bytes)
}

// TestReap_KeepMarkerExemptsTheSession: the keep marker row is the one
// hand-placed override, honoured under every scope.
func TestReap_KeepMarkerExemptsTheSession(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	require.NoError(t, os.WriteFile(l.KeepMarker("aged-quiet-heron"), nil, 0o644))
	reapBackdate(t, dir)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Scope: paths.Persist, Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Kept)
	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, ReapKept, rep.Candidates[0].Verdict)
	assert.Contains(t, rep.Candidates[0].Reason, paths.SessionKeepMarkerFileName)
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_SkipsWhenTheLockDoesNotProveTheOwnerDead: liveness comes from
// the Locks port, and the port only ever refuses — whatever it could not
// prove dead (held, missing, untrusted) is skipped with its reason, and its
// owner's pid is reported for a human.
func TestReap_SkipsWhenTheLockDoesNotProveTheOwnerDead(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	locks := deadLocks()
	locks.refused["aged-quiet-heron"] = "the session's lock is held: its owner is alive"

	rep, err := Reap(context.Background(), l, locks, ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Skipped)
	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, ReapSkipped, rep.Candidates[0].Verdict)
	assert.Equal(t, locks.pid, rep.Candidates[0].OwnerPID)
	assert.Contains(t, rep.Candidates[0].Reason, "alive")
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_HoldsTheLockAcrossTheRemoval: the lock is released only after
// the members are gone, so a session resuming under the same harp mid-reap
// waits rather than racing the deletion.
func TestReap_HoldsTheLockAcrossTheRemoval(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	locks := deadLocks()
	released := false
	locks.onRelease = func(harp string) {
		released = true
		assert.NoDirExists(t, filepath.Join(dir, paths.EphemeralDirName), "released before the removal finished")
	}

	_, err := Reap(context.Background(), l, locks, ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.True(t, released, "the hold must be released once the reap is done with the session")
}

// TestReap_LeavesASessionNewerThanTheBound: a session active since the bound
// is counted, not listed, and nothing of it is touched.
func TestReap_LeavesASessionNewerThanTheBound(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "young-quiet-heron")
	now := time.Now()
	require.NoError(t, os.Chtimes(filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName), now, now))

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Newer)
	assert.Empty(t, rep.Candidates)
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_IgnoresASessionWithNothingInScope: a session whose in-scope
// members are absent or empty is not a candidate at all — neither listed nor
// counted as newer.
func TestReap_IgnoresASessionWithNothingInScope(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "bare-quiet-heron")
	for _, m := range paths.HarpMembers {
		if m.Lifetime == paths.Ephemeral {
			require.NoError(t, os.RemoveAll(l.Member("bare-quiet-heron", m)))
		}
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.EphemeralDirName), 0o755))
	reapBackdate(t, dir)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Empty(t, rep.Candidates)
	assert.Equal(t, 0, rep.Newer)
	assert.FileExists(t, filepath.Join(dir, paths.SessionSidecarFileName))
}

// TestReap_RefusesASymlinkedMember: a member that is itself a symlink is
// reported and left in place; following it would turn a reap of disposable
// session state into a delete of whatever it points at.
func TestReap_RefusesASymlinkedMember(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "precious.txt"), []byte("not yours\n"), 0o644))
	require.NoError(t, os.RemoveAll(filepath.Join(dir, paths.EphemeralDirName)))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, paths.EphemeralDirName)))
	reapBackdate(t, dir)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Skipped)
	require.Len(t, rep.Candidates, 1)
	assert.Contains(t, rep.Candidates[0].Reason, "symlink")
	assert.FileExists(t, filepath.Join(target, "precious.txt"), "the link's target is never entered")
	_, lerr := os.Lstat(filepath.Join(dir, paths.EphemeralDirName))
	assert.NoError(t, lerr, "the link itself stays")
	assert.DirExists(t, filepath.Join(dir, paths.SessionEngineHomesDirName), "a refused session loses nothing, not even its other members")
}

// TestReap_DoesNotFollowASymlinkInsideAMember: a link deeper inside a taken
// member is unlinked with it; its target is never entered.
func TestReap_DoesNotFollowASymlinkInsideAMember(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "precious.txt"), []byte("not yours\n"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, paths.EphemeralDirName, "link")))
	reapBackdate(t, dir)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Reclaimed)
	assert.NoDirExists(t, filepath.Join(dir, paths.EphemeralDirName))
	assert.FileExists(t, filepath.Join(target, "precious.txt"))
}

// TestReap_IgnoresASymlinkedHarpDir: a symlink at the sessions root is not a
// candidate, whatever it points at.
func TestReap_IgnoresASymlinkedHarpDir(t *testing.T) {
	l := reapLayout(t)
	real := reapSeed(t, l, "aged-quiet-heron")
	require.NoError(t, os.Symlink(real, l.Dir("other-quiet-heron")))

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff()}, nil)
	require.NoError(t, err)

	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, "aged-quiet-heron", rep.Candidates[0].Harp)
}

// TestReap_TouchesNothingBesideTheSessions: directories whose names
// harp.Validate refuses are not candidates, and nothing under them moves.
func TestReap_TouchesNothingBesideTheSessions(t *testing.T) {
	l := reapLayout(t)
	reapSeed(t, l, "aged-quiet-heron")
	stray := filepath.Join(l.SessionsRoot(), "not:a:harp")
	require.NoError(t, os.MkdirAll(filepath.Join(stray, paths.EphemeralDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stray, paths.EphemeralDirName, "x"), []byte("x"), 0o644))
	reapBackdate(t, stray)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	require.Len(t, rep.Candidates, 1)
	assert.FileExists(t, filepath.Join(stray, paths.EphemeralDirName, "x"))
}

// TestReap_TriageSparesTheSession: the triage is the adapter's chance to
// say something under the taken members must be preserved — a scratch
// worktree holding uncommitted work. A spared session is reported and loses
// nothing.
func TestReap_TriageSparesTheSession(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	var sawProbe LockProbe
	triage := func(_ context.Context, harp string, probe LockProbe, apply bool) (string, error) {
		assert.Equal(t, "aged-quiet-heron", harp)
		assert.True(t, apply)
		sawProbe = probe
		return "its scratch worktree wt-1 must be preserved: uncommitted work", nil
	}

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, triage)
	require.NoError(t, err)

	assert.True(t, sawProbe.Dead, "the triage is handed the reaper's own probe, not asked to re-probe under its hold")
	assert.Equal(t, 1, rep.Spared)
	require.Len(t, rep.Candidates, 1)
	assert.Equal(t, ReapSpared, rep.Candidates[0].Verdict)
	assert.Contains(t, rep.Candidates[0].Reason, "wt-1")
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_TriageErrorSkipsTheSession: a triage that cannot decide leaves
// the session alone — "cannot determine" is never permission.
func TestReap_TriageErrorSkipsTheSession(t *testing.T) {
	l := reapLayout(t)
	dir := reapSeed(t, l, "aged-quiet-heron")
	triage := func(context.Context, string, LockProbe, bool) (string, error) {
		return "", errors.New("git is not installed")
	}

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, triage)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.Skipped)
	require.Len(t, rep.Candidates, 1)
	assert.Contains(t, rep.Candidates[0].Reason, "git is not installed")
	reapAssertPresence(t, dir, func(string, paths.HarpMember) bool { return false })
}

// TestReap_MissingSessionsRootIsNotAFault: nothing has ever run here.
func TestReap_MissingSessionsRootIsNotAFault(t *testing.T) {
	l := reapLayout(t)

	rep, err := Reap(context.Background(), l, deadLocks(), ReapPolicy{Cutoff: reapCutoff(), Apply: true}, nil)
	require.NoError(t, err)

	assert.Empty(t, rep.Candidates)
}

// --- ActivityTime: the one clock --------------------------------------------

// activityFixture plants a session with every mtime at base and returns its
// dir. A lock file is written beside the dir too, stamped now: it lives
// OUTSIDE the session dir and must not count.
func activityFixture(t *testing.T, l Layout, harp string, base time.Time) string {
	t.Helper()
	dir := l.Dir(harp)
	for _, rel := range []string{
		paths.SessionSidecarFileName,
		paths.PersistDirName + "/" + paths.CanonicalTranscriptFileName,
		paths.SegmentsDirName + "/abc.jsonl",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}
	require.NoError(t, filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, base, base)
	}))
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lock, []byte("4242\n"), 0o644))
	return dir
}

// TestActivityTime_IsTheNewestMemberMtime: writing a real member moves the
// clock to that write.
func TestActivityTime_IsTheNewestMemberMtime(t *testing.T) {
	l := reapLayout(t)
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	dir := activityFixture(t, l, "aged-quiet-heron", base)

	got, err := ActivityTime(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, got.Equal(base), "every member sits at base; got %s", got)

	later := base.Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName), later, later))

	got, err = ActivityTime(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, got.Equal(later), "writing a member is activity; got %s", got)
}

// TestActivityTime_IgnoresTheHarpDirsOwnMtime is the gate: the harp dir's
// mtime bumps whenever a direct child is created or removed — a reap that
// just emptied it, a lock probe — so counting it would make a session read
// as active for another whole bound after nothing happened in it.
func TestActivityTime_IgnoresTheHarpDirsOwnMtime(t *testing.T) {
	l := reapLayout(t)
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	dir := activityFixture(t, l, "aged-quiet-heron", base)
	now := time.Now()
	require.NoError(t, os.Chtimes(dir, now, now))

	got, err := ActivityTime(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, got.Equal(base), "a bumped harp-dir mtime is not activity; got %s", got)
}

// TestActivityTime_IgnoresASymlinksOwnMtime is the gate: a link's mtime is
// the link's, never a write through it, and the target is not followed. The
// engine transcript link is exactly this shape, at the top of the dir where
// only the excluded harp-dir mtime bumps with it.
func TestActivityTime_IgnoresASymlinksOwnMtime(t *testing.T) {
	l := reapLayout(t)
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	dir := activityFixture(t, l, "aged-quiet-heron", base)
	target := filepath.Join(t.TempDir(), "vendor.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("fresh\n"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, reapLinkName)))

	got, err := ActivityTime(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, got.Equal(base), "a touched symlink is not activity; got %s", got)
}

// TestActivityTime_IgnoresTheLockFile: the lock lives beside the session dir
// and is stamped by every probe; it is not activity in the session.
func TestActivityTime_IgnoresTheLockFile(t *testing.T) {
	l := reapLayout(t)
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	activityFixture(t, l, "aged-quiet-heron", base)

	got, err := ActivityTime(l, "aged-quiet-heron")
	require.NoError(t, err)
	assert.True(t, got.Equal(base), "the lock file (stamped now) must not count; got %s", got)
}

// TestActivityTime_MissingSessionIsAnError: a session that is not there has
// no clock; the caller decides what to fall back to.
func TestActivityTime_MissingSessionIsAnError(t *testing.T) {
	l := reapLayout(t)

	_, err := ActivityTime(l, "gone-quiet-heron")
	require.Error(t, err)
}

// TestReapPolicy_MembersFollowTheTable: the members a policy takes are
// derived from paths.HarpMembers — every Ephemeral row under the default
// scope, and the persist store besides under Scope Persist.
func TestReapPolicy_MembersFollowTheTable(t *testing.T) {
	var ephemeral []string
	for _, m := range paths.HarpMembers {
		if m.Lifetime == paths.Ephemeral {
			ephemeral = append(ephemeral, m.Rel())
		}
	}
	assert.Equal(t, ephemeral, ReapPolicy{}.MemberRels())
	assert.Equal(t, ephemeral, ReapPolicy{Scope: paths.Ephemeral}.MemberRels())

	persist := ReapPolicy{Scope: paths.Persist}.MemberRels()
	assert.Contains(t, persist, paths.PersistDirName)
	for _, rel := range ephemeral {
		assert.Contains(t, persist, rel)
	}
	assert.NotContains(t, persist, paths.SessionSidecarFileName, "the identity row is never taken")
	assert.NotContains(t, persist, paths.SegmentsDirName, "the derived segments are never taken")
	assert.NotContains(t, persist, paths.EssenceFileName, "the essence is never taken")
}
