package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A per-session instance home holds a COPIED CREDENTIAL, so every assertion in
// this file is a payload assertion about that file's bytes — never a bare
// "the directory is gone". A sweep that removed the directory but left the
// credential (or that reported a removal it never performed) is exactly the
// silent no-op this project keeps producing.
const instanceCredential = "sk-copied-from-the-real-host-home\n"

// seedInstance builds <projectDir>/.ctxloom/state/<harp>/home/claude/
// .credentials.json — the S5 instance shape, credential and all — and returns
// the instance root (state/<harp>) and the credential path.
func seedInstance(t *testing.T, projectDir, harp string) (root, credential string) {
	t.Helper()
	root, err := paths.SessionStatePath(filepath.Join(projectDir, paths.AppDirName), harp)
	require.NoError(t, err)
	credential = filepath.Join(root, paths.SessionHomeDirName, "claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(credential), 0o700))
	require.NoError(t, os.WriteFile(credential, []byte(instanceCredential), 0o600))
	// Guard against a vacuous assertion later: the fixture must really exist.
	require.FileExists(t, credential)
	return root, credential
}

// liveSession registers harp in the session index for projectDir and leaves it
// UNENDED — the index's own definition of a live session.
func liveSession(t *testing.T, projectDir string) string {
	t.Helper()
	mgr, err := sessions.Open()
	require.NoError(t, err)
	e, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	return e.HarpName
}

// endedSession registers harp, runs it under the liveness lock, and marks it
// ended with the lock released — the on-disk state a session leaves when it
// reached EndSession but its instance removal did not (Release keeps the
// file; unlocked IS the dead signal). This is the backstop's ordinary prey.
func endedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := crashedSession(t, projectDir)
	markEnded(t, harp)
	return harp
}

// locklessEndedSession registers harp and marks it ended WITHOUT ever holding
// its lock: a session from before the lock existed. Nothing can prove its
// owner dead, so the sweep must leave it alone.
func locklessEndedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	markEnded(t, harp)
	return harp
}

// resumedSession registers harp, marks it ended, and then HOLDS its lock for
// the rest of the test — a session resumed under its harp: EndedAt is still
// set from the earlier end, and its owner is running right now.
func resumedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	markEnded(t, harp)
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
	return harp
}

func markEnded(t *testing.T, harp string) {
	t.Helper()
	mgr, err := sessions.Open()
	require.NoError(t, err)
	require.NoError(t, mgr.MarkEnded(harp, time.Now()))
}

// crashedSession registers harp UNENDED in the index — exactly what a session
// whose process died before EndSession leaves behind — with a liveness lock
// file that nothing holds. Hold-then-Release produces the identical on-disk
// state a SIGKILL does (the kernel drops the lock; the file and its pid
// remain), without needing a process to kill.
func crashedSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
	return harp
}

// heldSession registers harp UNENDED and holds its liveness lock for the
// rest of the test — a session that is genuinely running right now.
func heldSession(t *testing.T, projectDir string) string {
	t.Helper()
	harp := liveSession(t, projectDir)
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
	return harp
}

// TestReapOrphanedSessionHomes_ReapsACrashedSession is the row's own case:
// the index still says nil EndedAt (the process died before it could say
// otherwise), but the liveness lock is FREE, so the owner is dead and the
// credential must not stay on disk. Before the lock existed this instance
// was un-reapable forever.
//
// MUTATION: invert the lock sense (treat a free lock as alive) → red; treat
// a nil EndedAt as live without consulting the lock → red.
func TestReapOrphanedSessionHomes_ReapsACrashedSession(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	crashed := crashedSession(t, projectDir)
	root, cred := seedInstance(t, projectDir, crashed)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoFileExists(t, cred, "a crashed session's copied credential must not stay on disk")
	assert.NoDirExists(t, root)
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 0, res.Skipped)
}

// TestReapOrphanedSessionHomes_SkipsAHeldSession is the lock's refusal: a
// nil-EndedAt session whose lock is HELD is running, and its instance is the
// config its engine is running against. Beside it, one with NO lock file at
// all (a session from before the lock existed) is indeterminate and equally
// untouched — "cannot determine" is never permission.
//
// MUTATION: invert the lock sense (treat a held lock as dead) → red; treat
// a missing lock as dead → red.
func TestReapOrphanedSessionHomes_SkipsAHeldSession(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	held := heldSession(t, projectDir)
	heldRoot, heldCred := seedInstance(t, projectDir, held)
	unlocked := liveSession(t, projectDir) // index-live, no lock file ever written
	unlockedRoot, unlockedCred := seedInstance(t, projectDir, unlocked)
	crashed := crashedSession(t, projectDir)
	crashedRoot, _ := seedInstance(t, projectDir, crashed)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.FileExists(t, heldCred, "a running session's instance is never touched")
	assert.DirExists(t, heldRoot)
	assert.FileExists(t, unlockedCred, "a session with no lock cannot be proven dead, so it is left alone")
	assert.DirExists(t, unlockedRoot)
	assert.NoDirExists(t, crashedRoot, "the crashed sibling proves the sweep did run")
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 2, res.Skipped)
}

// TestEndSession_RemovesThisSessionsInstance is the teardown half: ending a
// session removes ITS instance and nothing else.
//
// The surviving sibling is the vacuity guard. Without it a teardown that wiped
// the whole state/ tree — or a test whose fixture never landed on disk — would
// pass just as happily as the correct one.
//
// MUTATION (m3): drop the removeSessionInstance call from EndSession → red.
func TestEndSession_RemovesThisSessionsInstance(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()

	ending := liveSession(t, projectDir)
	sibling := liveSession(t, projectDir)
	endingRoot, endingCred := seedInstance(t, projectDir, ending)
	siblingRoot, siblingCred := seedInstance(t, projectDir, sibling)

	require.NoError(t, EndSession(ending, time.Now()))

	assert.NoFileExists(t, endingCred,
		"the ending session's COPIED CREDENTIAL must not survive its own session")
	assert.NoDirExists(t, endingRoot,
		"the ending session's whole instance root must go, not just its credential")

	assert.DirExists(t, siblingRoot,
		"a concurrent session's instance must survive another session's teardown")
	got, err := os.ReadFile(siblingCred)
	require.NoError(t, err)
	assert.Equal(t, instanceCredential, string(got),
		"the surviving session's credential must be byte-identical — this is the vacuity guard for every removal assertion above")

	// The end-mark itself still happened: teardown is an ADDITION to the
	// index write, never a replacement for it (the sweep's backstop reads it).
	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.Find(ending)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.NotNil(t, entry.EndedAt, "EndSession must still stamp EndedAt")
}

// TestEndSession_UnknownHarpTouchesNothing pins that a harp the index never
// knew about is an error with NO filesystem effect: the ProjectDir teardown
// needs comes from the index entry, so there is nothing to resolve, and
// guessing would be a project-wide delete keyed by an unvalidated string.
func TestEndSession_UnknownHarpTouchesNothing(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()

	live := liveSession(t, projectDir)
	root, cred := seedInstance(t, projectDir, live)

	require.Error(t, EndSession("never-assigned-harp", time.Now()))

	assert.DirExists(t, root)
	got, err := os.ReadFile(cred)
	require.NoError(t, err)
	assert.Equal(t, instanceCredential, string(got))
}

// TestReapOrphanedSessionHomes_ReapsEndedSession is the crash backstop's
// positive case: a session the index says has ENDED, whose lock is FREE and
// whose instance is still on disk (teardown never ran, or failed), is removed.
func TestReapOrphanedSessionHomes_ReapsEndedSession(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	orphan := endedSession(t, projectDir)
	root, cred := seedInstance(t, projectDir, orphan)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoFileExists(t, cred, "an ended session's copied credential must not stay on disk")
	assert.NoDirExists(t, root)
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 0, res.Skipped)
}

// TestReapOrphanedSessionHomes_SkipsAResumedSession is the case ended_at gets
// exactly wrong in the destructive direction: a session RESUMED under its harp
// has EndedAt still set from the earlier end, and its lock is HELD by the
// process running now. Its instance is the config (and credential) that
// engine is running against. The index's timestamp is not permission; only a
// free lock is.
//
// The ended sibling with a free lock is the vacuity guard: it proves the sweep
// ran and could remove an ended session — so the survivor survived because
// of the lock, not because nothing happened.
//
// MUTATION: reap index-ended candidates without probing the lock → red.
func TestReapOrphanedSessionHomes_SkipsAResumedSession(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	resumed := resumedSession(t, projectDir)
	resumedRoot, resumedCred := seedInstance(t, projectDir, resumed)
	ended := endedSession(t, projectDir)
	endedRoot, _ := seedInstance(t, projectDir, ended)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.DirExists(t, resumedRoot, "a resumed session's instance must survive: its lock is held, whatever EndedAt says")
	got, rerr := os.ReadFile(resumedCred)
	require.NoError(t, rerr)
	assert.Equal(t, instanceCredential, string(got),
		"the resumed session's credential must be byte-identical after the sweep")
	assert.NoDirExists(t, endedRoot, "the ended sibling proves the sweep did run")
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 1, res.Skipped)
}

// TestReapOrphanedSessionHomes_SkipsALocklessEndedSession pins the accepted
// cost of asking the lock for every candidate: a session that predates the
// lock has an EndedAt and no lock file, so nothing can prove its owner dead,
// and "cannot determine" is never permission. Its credential stays until the
// harp is resumed and ended under the lock, or purged deliberately — the same
// price `clean --older-than` already states in its help.
//
// MUTATION: treat a non-nil EndedAt as permission → red.
func TestReapOrphanedSessionHomes_SkipsALocklessEndedSession(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	lockless := locklessEndedSession(t, projectDir)
	locklessRoot, locklessCred := seedInstance(t, projectDir, lockless)
	ended := endedSession(t, projectDir)
	endedRoot, _ := seedInstance(t, projectDir, ended)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.FileExists(t, locklessCred, "no lock file means the owner cannot be proven dead; the instance is left alone")
	assert.DirExists(t, locklessRoot)
	assert.NoDirExists(t, endedRoot, "the ended sibling proves the sweep did run")
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 1, res.Skipped)
}

// TestReapOrphanedSessionHomes_ReapsHarpAbsentFromTheIndex covers the other
// ended shape: an instance whose harp the index no longer carries at all
// (`session forget`, a hand-edited index, a pre-index checkout) but whose
// lock file survives, free. The index's silence is not what permits the
// reap; the free lock is — an absent harp is probed like every other.
func TestReapOrphanedSessionHomes_ReapsHarpAbsentFromTheIndex(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	const forgotten = "forgotten-quiet-heron"
	require.NoError(t, sessionlock.Hold(forgotten))
	sessionlock.Release(forgotten)
	root, cred := seedInstance(t, projectDir, forgotten)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoFileExists(t, cred)
	assert.NoDirExists(t, root)
	assert.Equal(t, 1, res.Reaped)
}

// TestReapOrphanedSessionHomes_SkipsLiveSession is the safety half, and the one
// that matters most: a concurrent session's sweep must never yank a LIVE
// session's engine config out from under its running engine.
//
// MUTATION (m1): reap live sessions too (drop the liveness check) → red.
func TestReapOrphanedSessionHomes_SkipsLiveSession(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	live := liveSession(t, projectDir)
	root, cred := seedInstance(t, projectDir, live)
	dead := endedSession(t, projectDir)
	deadRoot, _ := seedInstance(t, projectDir, dead)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.DirExists(t, root, "a live session's instance must survive another session's sweep")
	got, err := os.ReadFile(cred)
	require.NoError(t, err)
	assert.Equal(t, instanceCredential, string(got),
		"the live session's engine config must be byte-identical after a concurrent sweep")

	// The dead sibling is the vacuity guard: it proves the sweep actually ran
	// and was capable of removing something.
	assert.NoDirExists(t, deadRoot)
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 1, res.Skipped)
}

// TestReapOrphanedSessionHomes_LeavesStateRootFilesAlone pins the first
// exclusion: state/ root FILES are project-scoped local data whose loss is real
// (the tier table says so, and "free to delete" applies only to state/<harp>/).
//
// The third fixture is the one that makes the exclusion load-bearing rather
// than decorative. A harp is a USER-RENAMEABLE string (`ctxloom session edit
// <old> --name …`) that becomes a path component, and harp.Validate accepts
// "dirty_tree_commit_ack.yaml" happily — so a root file's name CAN be a name
// the index knows, and when it is, "is a directory" is the only thing standing
// between this sweep and the human's own project state.
//
// MUTATION (m2): drop the IsDir gate so root files become candidates → red.
func TestReapOrphanedSessionHomes_LeavesStateRootFilesAlone(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	// A reapable orphan alongside, so a sweep that removed nothing at all
	// cannot pass this test by doing nothing.
	orphan := endedSession(t, projectDir)
	orphanRoot, _ := seedInstance(t, projectDir, orphan)

	stateDir := paths.StatePath(appPath)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	ack := paths.DirtyTreeCommitAckPath(appPath)
	const ackBody = "acknowledged: true\n"
	require.NoError(t, os.WriteFile(ack, []byte(ackBody), 0o600))
	stray := filepath.Join(stateDir, "some-future-root-file.yaml")
	require.NoError(t, os.WriteFile(stray, []byte("keep me\n"), 0o600))

	// A root FILE at state/<harp> for a harp the index knows and has ended:
	// every candidate rule except "is a directory" says reap it.
	collided := endedSession(t, projectDir)
	collidedFile := filepath.Join(stateDir, collided)
	const collidedBody = "not an instance — a file that happens to share a harp's name\n"
	require.NoError(t, os.WriteFile(collidedFile, []byte(collidedBody), 0o600))

	_, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoDirExists(t, orphanRoot, "the sweep must have actually run")
	got, err := os.ReadFile(ack)
	require.NoError(t, err)
	assert.Equal(t, ackBody, string(got),
		"the dirty-tree commit acknowledgement is project-scoped state whose loss is real")
	assert.FileExists(t, stray)
	gotCollided, err := os.ReadFile(collidedFile)
	require.NoError(t, err)
	assert.Equal(t, collidedBody, string(gotCollided),
		"a FILE under state/ is never an instance, however harp-shaped and index-known its name is")
}

// TestReapOrphanedSessionHomes_LeavesFixedResidentsAlone pins the second
// exclusion: locks/ and trust/ are FIXED project-scoped residents of state/
// (the arch roster in tests/arch/session_home_arch_test.go enumerates exactly
// these), and both names pass harp validation.
func TestReapOrphanedSessionHomes_LeavesFixedResidentsAlone(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	orphan := endedSession(t, projectDir)
	orphanRoot, _ := seedInstance(t, projectDir, orphan)

	lock := filepath.Join(paths.LocksPath(appPath), "config.yaml.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, []byte("held\n"), 0o600))

	trustObject := filepath.Join(paths.TrustObjectsPath(appPath), "abc123")
	require.NoError(t, os.MkdirAll(filepath.Dir(trustObject), 0o755))
	require.NoError(t, os.WriteFile(trustObject, []byte("signed\n"), 0o600))

	_, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoDirExists(t, orphanRoot, "the sweep must have actually run")
	assert.FileExists(t, lock, "state/locks is a fixed project-scoped resident, not a session key")
	assert.FileExists(t, trustObject, "state/trust is a fixed project-scoped resident, not a session key")
}

// TestReapOrphanedSessionHomes_LeavesNonInstanceDirsAlone pins the third
// exclusion: a directory under state/ that is not shaped like an instance is
// never recursed into or removed, however harp-like its name reads. The RETIRED
// durable per-project engine home (state/engines) is the concrete case — the
// name passes harp validation, and it is absent from the index, so nothing but
// the structural check keeps it out of the candidate set.
func TestReapOrphanedSessionHomes_LeavesNonInstanceDirsAlone(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	orphan := endedSession(t, projectDir)
	orphanRoot, _ := seedInstance(t, projectDir, orphan)

	retired := filepath.Join(paths.StatePath(appPath), "engines", "codex", ".codex", "auth.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(retired), 0o700))
	require.NoError(t, os.WriteFile(retired, []byte("legacy\n"), 0o600))

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoDirExists(t, orphanRoot, "the sweep must have actually run")
	assert.FileExists(t, retired,
		"state/engines is the retired durable home, not a session instance; this sweep is not the thing that gets to decide its fate")
	assert.Equal(t, 1, res.Reaped)
	assert.Equal(t, 0, res.Skipped,
		"a non-instance directory is not a candidate at all, so it is not even counted as skipped")
}

// TestReapOrphanedSessionHomes_NeverFollowsASymlink pins the fourth exclusion.
// A symlink under state/ whose name reads like a harp must not be followed:
// following one turns a sweep of a disposable project directory into a delete
// of whatever the link points at.
//
// MUTATION (m4): follow symlinks (stat instead of lstat when classifying, or
// resolve the target before removing) → red.
func TestReapOrphanedSessionHomes_NeverFollowsASymlink(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	orphan := endedSession(t, projectDir)
	orphanRoot, _ := seedInstance(t, projectDir, orphan)

	// The link target is shaped EXACTLY like an instance (it has a home/
	// subdirectory holding a credential) and its harp is absent from the
	// index — so every rule except "never follow a symlink" says reap it.
	outside := t.TempDir()
	precious := filepath.Join(outside, paths.SessionHomeDirName, "claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(precious), 0o700))
	require.NoError(t, os.WriteFile(precious, []byte(instanceCredential), 0o600))

	link := filepath.Join(paths.StatePath(appPath), "sneaky-linked-harp")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(outside, link))

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)

	assert.NoDirExists(t, orphanRoot, "the sweep must have actually run")
	got, err := os.ReadFile(precious)
	require.NoError(t, err)
	assert.Equal(t, instanceCredential, string(got),
		"a symlink's TARGET must be byte-identical after the sweep — following one would delete data outside the project")
	_, lerr := os.Lstat(link)
	assert.NoError(t, lerr, "the link itself is left in place too; the sweep removes instances, not links")
	assert.Equal(t, 1, res.Reaped)
}

// TestReapOrphanedSessionHomes_AbsentStateDirIsAQuietZero: a project that has
// never run a controlled-home session has no state/ at all, and a startup sweep
// must treat that as "nothing to do", not as a fault.
func TestReapOrphanedSessionHomes_AbsentStateDirIsAQuietZero(t *testing.T) {
	testsupport.Isolate(t)
	appPath := filepath.Join(t.TempDir(), paths.AppDirName)

	res, err := ReapOrphanedSessionHomes(appPath)
	require.NoError(t, err)
	assert.Equal(t, SessionHomeReapResult{}, res)
}

// TestReapOrphanedSessionHomes_UnreadableIndexRemovesNothing is the fail-safe
// direction: without the index there is no liveness signal, and a sweep with no
// liveness signal would reap every live session in the project. It must report
// the fault and touch nothing.
func TestReapOrphanedSessionHomes_UnreadableIndexRemovesNothing(t *testing.T) {
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	appPath := filepath.Join(projectDir, paths.AppDirName)

	live := liveSession(t, projectDir)
	root, cred := seedInstance(t, projectDir, live)

	// A regular file where the sessions root should be: every enumeration
	// fails with ENOTDIR, for root and non-root alike.
	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(sessionsRoot))
	require.NoError(t, os.WriteFile(sessionsRoot, []byte("not a directory\n"), 0o600))

	res, err := ReapOrphanedSessionHomes(appPath)
	require.Error(t, err, "an unreadable session store must be reported, never treated as an empty one")
	assert.Equal(t, SessionHomeReapResult{}, res)

	assert.DirExists(t, root)
	got, rerr := os.ReadFile(cred)
	require.NoError(t, rerr)
	assert.Equal(t, instanceCredential, string(got))
}
