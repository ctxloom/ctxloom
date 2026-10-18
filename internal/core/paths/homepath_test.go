package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestHomePathFor_LandsUnderHomeLocks pins the shape ruled 2026-08-13
// (closing undated-bronco / fs-consolidation N1): a lock guarding a FOREIGN
// file — one outside any project .ctxloom tree, incl. inside the user's real
// engine homes — lives under ~/.ctxloom/locks, never beside the file itself.
// Beside-the-file (PathFor) is exactly what left `.mcp.json.lock` and
// `~/.claude/settings.json.lock` as untracked litter.
func TestHomePathFor_LandsUnderHomeLocks(t *testing.T) {
	home := testsupport.Isolate(t)
	protected := filepath.Join(home, "project", ".claude", "settings.json")

	got, err := HomePathFor(protected)
	require.NoError(t, err)

	wantDir := filepath.Join(home, ".ctxloom", "locks")
	assert.Equal(t, wantDir, filepath.Dir(got),
		"the lock must sit in the home locks directory, not beside the protected file")
	assert.NotEqual(t, PathFor(protected), got,
		"the home mapping must not collapse back to the beside-the-file shape")
}

// TestHomePathFor_FlattensDistinctPathsToDistinctNames is the mutation-kill
// test for the flatten step: MUTATION — replace FlatName's call
// inside HomePathFor with the bare absolute path (or drop the flatten
// entirely) — turns this red, because the lock for a NESTED protected file
// would then land in a directory that mirrors the real tree instead of
// beside its sibling in ONE flat locks directory, and the two distinct
// targets below would no longer be guaranteed to differ only in their
// (single-component) basename.
func TestHomePathFor_FlattensDistinctPathsToDistinctNames(t *testing.T) {
	home := testsupport.Isolate(t)

	nested, err := HomePathFor(filepath.Join(home, "proj", "a", ".claude", "settings.json"))
	require.NoError(t, err)
	other, err := HomePathFor(filepath.Join(home, "proj", "b", ".mcp.json"))
	require.NoError(t, err)

	wantDir := filepath.Join(home, ".ctxloom", "locks")
	assert.Equal(t, wantDir, filepath.Dir(nested),
		"flattening must keep every home lock in ONE directory; a lock tree mirroring the real one is a second convention")
	assert.Equal(t, wantDir, filepath.Dir(other))
	assert.NotEqual(t, nested, other,
		"two distinct protected files must not be handed the same lock by accident")
}

// TestHomePathFor_SpellingsOfOneFileMapToOneLock covers the same invariant
// ProjectPathFor's identically-named test covers for the project case: every
// spelling a caller can plausibly hold of one file — with a dot segment, a
// dot-dot round trip, doubled separators — must resolve to the SAME lock, or
// two writers of the identical file silently fail to exclude each other.
func TestHomePathFor_SpellingsOfOneFileMapToOneLock(t *testing.T) {
	home := testsupport.Isolate(t)
	target := filepath.Join(home, "proj", ".claude", "settings.json")

	spellings := []string{
		target,
		filepath.Join(home, "proj", ".", ".claude", "settings.json"),
		filepath.Join(home, "proj", "other", "..", ".claude", "settings.json"),
		filepath.Join(home, "proj") + string(filepath.Separator) + string(filepath.Separator) + filepath.Join(".claude", "settings.json"),
	}

	want, err := HomePathFor(target)
	require.NoError(t, err)
	for _, spelling := range spellings {
		got, err := HomePathFor(spelling)
		require.NoError(t, err)
		assert.Equal(t, want, got, "%q and %q name the same file; two lock paths means two writers that do not exclude each other", spelling, target)
	}
}

// TestHomePathFor_DistinctPathsGetDistinctLocks pins that flattening is
// injective in practice: two protected paths that differ only in WHERE a path
// separator falls relative to an underscore run used to flatten to ONE name
// and share a lock (documented then as safe over-serialization). The bounded
// name carries a hash of the whole path, so distinct resources get distinct
// locks. The invariant this file cannot give up is unchanged and pinned
// above: one resource, however spelled, gets ONE lock name.
func TestHomePathFor_DistinctPathsGetDistinctLocks(t *testing.T) {
	home := testsupport.Isolate(t)

	withSeparator, err := HomePathFor(filepath.Join(home, "a", "b", "c"))
	require.NoError(t, err)
	withUnderscore, err := HomePathFor(filepath.Join(home, "a", "b__c"))
	require.NoError(t, err)

	assert.NotEqual(t, withSeparator, withUnderscore,
		"a/b/c and a/b__c are different files and must not share a lock name")
}

// TestHomePathFor_FailsClosedWhenHomeCannotBeResolved pins the home-resolution
// error path: os.UserHomeDir failing must propagate rather than fall back to
// a guessed location, and the failure must name what was being resolved.
func TestHomePathFor_FailsClosedWhenHomeCannotBeResolved(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("HOME", "")

	got, err := HomePathFor("/some/foreign/settings.json")
	require.Error(t, err)
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), "/some/foreign/settings.json",
		"the error must name the protected file the lock was being derived for")
}

// sessionLockHarp is the session the session-lock tests place files in.
const sessionLockHarp = "swift-amber-falcon"

// TestHomePathFor_SessionFileLocksInItsOwnSessionDir: a file inside a session
// dir gets its lock in that session's own locks member, named for its path
// relative to the session dir, never in the shared home locks dir, where a
// lock nothing ever reuses outlived its session for good. The lock
// classifies to the Ephemeral locks row, which is what makes the reaper take
// it with the session.
func TestHomePathFor_SessionFileLocksInItsOwnSessionDir(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := HarpDir(sessionLockHarp)
	require.NoError(t, err)
	locksDir, err := HarpLocksDir(sessionLockHarp)
	require.NoError(t, err)
	homeLocks, err := HomeLocksDir()
	require.NoError(t, err)

	for _, rel := range []string{
		"home/claude",
		"home/claude/.claude.json",
		"home/claude/skills/demo/SKILL.md",
		"work/ctxloom-wt-agent/.mcp.json",
	} {
		got, err := HomePathFor(filepath.Join(harpDir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(locksDir, FlatName(rel)+lockSuffix), got,
			"%s must lock in its own session's locks dir, named for its path within the session", rel)
		assert.NotEqual(t, homeLocks, filepath.Dir(got), "%s must not lock in the shared home locks dir", rel)

		lockRel, err := filepath.Rel(harpDir, got)
		require.NoError(t, err)
		m, ok := ClassifyMember(filepath.ToSlash(lockRel))
		require.True(t, ok, "the lock of %s must be a member of its session", rel)
		assert.Equal(t, HarpLocksDirName, m.Name)
		assert.Equal(t, Ephemeral, m.Lifetime, "the reaper takes Ephemeral members only; the lock of %s must go with its session", rel)
	}
}

// TestHomePathFor_PathsOutsideASessionKeepTheHomeLocksDir: only a path
// strictly inside a harp dir moves. Everything else keeps the shared home
// locks dir: a project file, the real engine home, the sessions root and the
// loose files in it, a harp dir itself (a lock guarding a whole session dir
// must not live inside the dir it guards, as HarpLockPath's does not), the
// lock files beside a harp dir, a name under the root no harp can carry, and
// the sessions of a home that is not this process's.
func TestHomePathFor_PathsOutsideASessionKeepTheHomeLocksDir(t *testing.T) {
	home := testsupport.Isolate(t)
	root, err := HomeSessionsDir()
	require.NoError(t, err)
	harpDir, err := HarpDir(sessionLockHarp)
	require.NoError(t, err)
	liveness, err := HarpLockPath(sessionLockHarp)
	require.NoError(t, err)
	sidecarLock, err := HarpSidecarLockPath(sessionLockHarp)
	require.NoError(t, err)
	homeLocks, err := HomeLocksDir()
	require.NoError(t, err)

	for name, protected := range map[string]string{
		"a project file":                      filepath.Join(home, "proj", ".mcp.json"),
		"the real engine home":                filepath.Join(home, ".claude", "settings.json"),
		"the sessions root":                   root,
		"a loose file in the sessions root":   filepath.Join(root, IndexFileName),
		"a harp dir itself":                   harpDir,
		"the liveness lock beside a harp dir": liveness,
		"the sidecar lock beside a harp dir":  sidecarLock,
		"a name no harp can carry":            filepath.Join(root, " lead", "home", "settings.json"),
		"another home's session":              filepath.Join(t.TempDir(), AppDirName, SessionsDir, sessionLockHarp, "home", "settings.json"),
	} {
		got, err := HomePathFor(protected)
		require.NoError(t, err, name)
		assert.Equal(t, filepath.Join(homeLocks, FlatName(protected)+lockSuffix), got, "%s must keep the home locks dir", name)
	}
}

// TestHomePathFor_SymlinkedHomeSpellingsShareTheSessionLock: under a HOME
// that is a symlink a session file has two spellings: through $HOME, as
// every paths.Harp* helper builds it, and resolved, as git reports a
// worktree's path. Both must reach the ONE lock in the session's own locks
// dir. Two lock names for one file exclude nobody, and a lock that fell back
// to the home locks dir would outlive the session.
func TestHomePathFor_SymlinkedHomeSpellingsShareTheSessionLock(t *testing.T) {
	testsupport.Isolate(t)
	rel := filepath.Join("home", "claude", ".claude.json")
	realHome := filepath.Join(t.TempDir(), "real-home")
	require.NoError(t, os.MkdirAll(filepath.Join(realHome, AppDirName, SessionsDir, sessionLockHarp, filepath.Dir(rel)), 0o755))
	link := filepath.Join(t.TempDir(), "home-link")
	require.NoError(t, os.Symlink(realHome, link))
	testsupport.PointHomeAt(t, link)
	// Resolved fully: an OS temp root can itself sit behind a link.
	resolvedHome, err := filepath.EvalSymlinks(realHome)
	require.NoError(t, err)

	locksDir, err := HarpLocksDir(sessionLockHarp)
	require.NoError(t, err)
	want := filepath.Join(locksDir, FlatName(filepath.ToSlash(rel))+lockSuffix)

	viaHome, err := HomePathFor(filepath.Join(link, AppDirName, SessionsDir, sessionLockHarp, rel))
	require.NoError(t, err)
	assert.Equal(t, want, viaHome)
	resolved, err := HomePathFor(filepath.Join(resolvedHome, AppDirName, SessionsDir, sessionLockHarp, rel))
	require.NoError(t, err)
	assert.Equal(t, want, resolved, "the resolved spelling of a session file must reach the lock its $HOME spelling reaches")
}

// TestHomePathFor_SessionLockNamesStayDistinctWhenTruncated: FlatName bounds
// a name by keeping only its path's tail, so two deep files of one session
// whose paths share a long suffix truncate to the same readable tail. The
// hash of the whole session-relative path keeps their locks apart, in the
// one session's locks dir, at a bounded length.
func TestHomePathFor_SessionLockNamesStayDistinctWhenTruncated(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := HarpDir(sessionLockHarp)
	require.NoError(t, err)
	tail := filepath.FromSlash(strings.Repeat("deep/", 30) + "settings.json")

	a, err := HomePathFor(filepath.Join(harpDir, "home", "claude", tail))
	require.NoError(t, err)
	b, err := HomePathFor(filepath.Join(harpDir, "work", "ctxloom-wt-agent", tail))
	require.NoError(t, err)

	require.Equal(t, filepath.Dir(a), filepath.Dir(b), "both must lock in the one session's locks dir")
	readable := func(lock string) string {
		name := strings.TrimSuffix(filepath.Base(lock), lockSuffix)
		return name[:len(name)-len(flatSep)-flatHashLen]
	}
	require.Equal(t, readable(a), readable(b), "the fixture must truncate both paths to one readable tail, or it tests nothing")
	assert.NotEqual(t, a, b, "two files of one session must not share a lock because their names truncate alike")
	for _, lock := range []string{a, b} {
		assert.LessOrEqual(t, len(filepath.Base(lock)), flatTailMax+len(flatSep)+flatHashLen+len(lockSuffix))
	}
}

// TestHomeLockPathIn_IsHomePathForInAnotherFilesystem: the container mount
// builder binds the host's lock where the CONTAINER's own HomePathFor looks,
// which it computes with HomeLockPathIn over the container's home. That must
// be the same derivation, not a second one: for every kind of path
// HomePathFor tells apart, HomeLockPathIn over this process's own home gives
// what HomePathFor gives.
func TestHomeLockPathIn_IsHomePathForInAnotherFilesystem(t *testing.T) {
	home := testsupport.Isolate(t)
	harpDir, err := HarpDir(sessionLockHarp)
	require.NoError(t, err)
	liveness, err := HarpLockPath(sessionLockHarp)
	require.NoError(t, err)
	homeLocks, err := HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(homeLocks), HomeLocksDirIn(filepath.ToSlash(home)))

	for _, protected := range []string{
		filepath.Join(harpDir, "home", "claude", ".claude.json"),
		filepath.Join(harpDir, "work", "ctxloom-wt-agent", ".mcp.json"),
		harpDir,
		liveness,
		filepath.Join(home, "proj", ".mcp.json"),
	} {
		want, err := HomePathFor(protected)
		require.NoError(t, err)
		assert.Equal(t, filepath.ToSlash(want), HomeLockPathIn(filepath.ToSlash(home), filepath.ToSlash(protected)), protected)
	}
}

// TestHomeLockPathIn_SplitsOnTheNamedHomesSessions: in a container whose home
// is not the host's, a file under the CONTAINER's own sessions root locks in
// that session's dir, while a host-shaped path the container reaches through
// an identity mount lies in no session of the container's and keeps its
// home locks dir. abs is cleaned first, as HomePathFor cleans it.
func TestHomeLockPathIn_SplitsOnTheNamedHomesSessions(t *testing.T) {
	const ctrHome = "/home/ctxloom"
	inOwnSession := "/home/ctxloom/.ctxloom/sessions/" + sessionLockHarp + "/work/wt/.mcp.json"
	assert.Equal(t, "/home/ctxloom/.ctxloom/sessions/"+sessionLockHarp+"/locks/"+FlatName("work/wt/.mcp.json")+lockSuffix,
		HomeLockPathIn(ctrHome, inOwnSession))

	hostShaped := "/home/someone/.ctxloom/sessions/" + sessionLockHarp + "/work/wt/.mcp.json"
	assert.Equal(t, "/home/ctxloom/.ctxloom/locks/"+FlatName(hostShaped)+lockSuffix, HomeLockPathIn(ctrHome, hostShaped))
	assert.Equal(t, HomeLockPathIn(ctrHome, hostShaped),
		HomeLockPathIn(ctrHome, "/home/someone/./.ctxloom/sessions/other/../"+sessionLockHarp+"/work/wt/.mcp.json"))
}
