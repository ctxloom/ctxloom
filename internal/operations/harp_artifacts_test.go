package operations

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func writeHarpFile(t *testing.T, root, harp, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, harp)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// endedUnderLock leaves harp's liveness lock on disk and unheld: the state a
// session leaves once it has ended (or died) under the lock, and the only
// state the migration accepts as proof that nobody is writing the plan.
func endedUnderLock(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
}

// isolatedSessionsRoot isolates the environment and returns the sessions root
// the lock resolves against, so a harp written under it and its lock beside
// it describe the same session.
func isolatedSessionsRoot(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	return root
}

// TestHarpTopLevelArtifacts_NamesAuthoredWorkOnly pins the shared predicate
// cli.doctorCheckHarpDurability reports and MigrateHarpArtifacts moves. Every
// exclusion here is a file ctxloom itself writes at the top level by design;
// moving one would pull ctxloom's own bookkeeping out from under its readers,
// and flagging one would put a warning on every healthy session.
func TestHarpTopLevelArtifacts_NamesAuthoredWorkOnly(t *testing.T) {
	root := t.TempDir()
	const harp = "brisk-teal-otter"
	writeHarpFile(t, root, harp, "design"+paths.PlanFileExt, "authored")
	writeHarpFile(t, root, harp, "audit.md", "authored")
	writeHarpFile(t, root, harp, paths.EssenceFileName, "ctxloom's own")
	writeHarpFile(t, root, harp, paths.CanonicalTranscriptFileName, "ctxloom's own")
	writeHarpFile(t, root, harp, paths.LegacyCanonicalTranscriptFileName, "ctxloom's own")
	writeHarpFile(t, root, harp, paths.IndexFileName, "ctxloom's own")
	writeHarpFile(t, root, harp, paths.SessionKeepMarkerFileName, "")
	writeHarpFile(t, root, harp, paths.NextStepFileName, "ctxloom's own")
	writeHarpFile(t, root, harp, paths.EngineTranscriptLinkPrefix+"claude-abc.jsonl", "ctxloom's own")
	require.NoError(t, os.MkdirAll(filepath.Join(root, harp, paths.PersistDirName), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, harp, paths.EphemeralDirName), 0o755))

	got, err := HarpTopLevelArtifacts(filepath.Join(root, harp))
	require.NoError(t, err)
	assert.Equal(t, []string{"audit.md", "design" + paths.PlanFileExt}, got)
}

func TestHarpTopLevelArtifacts_MissingDirIsNotAFault(t *testing.T) {
	got, err := HarpTopLevelArtifacts(filepath.Join(t.TempDir(), "never-existed"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestMigrateHarpArtifacts_MovesTheBytes asserts the migration's actual
// effect: the file is READABLE at its new durable path with byte-identical
// content, and is GONE from the undurable one. A tally of "1 moved" with an
// empty file under persist/ would be the same silent no-op the move exists to
// prevent, so the content is what is checked.
func TestMigrateHarpArtifacts_MovesTheBytes(t *testing.T) {
	root := isolatedSessionsRoot(t)
	const harp = "brisk-teal-otter"
	const body = "# design\n\nthe decision and why\n"
	src := writeHarpFile(t, root, harp, "design"+paths.PlanFileExt, body)
	writeHarpFile(t, root, harp, paths.EssenceFileName, "ctxloom's own")
	endedUnderLock(t, harp)

	got, err := MigrateHarpArtifacts(root)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{Moved: 1}, got)

	dst := filepath.Join(root, harp, paths.PersistDirName, "design"+paths.PlanFileExt)
	moved, err := os.ReadFile(dst)
	require.NoError(t, err, "the plan must exist under persist/, where a container's bind mount reaches it")
	assert.Equal(t, body, string(moved), "with every byte of the original, not an empty file at the right path")

	_, err = os.Stat(src)
	assert.True(t, os.IsNotExist(err), "and it must be GONE from the undurable top level, not copied")

	_, err = os.Stat(filepath.Join(root, harp, paths.EssenceFileName))
	assert.NoError(t, err, "ctxloom's own top-level bookkeeping stays where its readers look")
}

// TestMigrateHarpArtifacts_SkipsAResumedSession is the case the index's
// ended_at gets exactly wrong: a session RESUMED under its harp has EndedAt
// still set from the earlier end, and its lock is HELD by the process running
// now. That agent holds the plan file's OLD path and will write to it again;
// moving the file mid-session does not relocate the plan, it forks it — half
// under persist/, the rest recreated at the top level by the next edit, and
// neither copy complete. The index's timestamp is not permission; only a free
// lock is.
//
// The ended sibling with a free lock is the vacuity guard: it proves the sweep
// ran and could move an ended session's file — so the resumed one survived
// because of the lock, not because nothing happened.
//
// MUTATION: decide liveness from ended_at (a set ended_at migrates) → red.
func TestMigrateHarpArtifacts_SkipsAResumedSession(t *testing.T) {
	root := isolatedSessionsRoot(t)
	projectDir := t.TempDir()

	resumed := resumedSession(t, projectDir)
	resumedPlan := writeHarpFile(t, root, resumed, "wip"+paths.PlanFileExt, "still being written")
	ended := endedSession(t, projectDir)
	endedPlan := writeHarpFile(t, root, ended, "done"+paths.PlanFileExt, "finished")

	got, err := MigrateHarpArtifacts(root)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{Moved: 1, RefusedHarps: 1}, got)

	body, err := os.ReadFile(resumedPlan)
	require.NoError(t, err, "the resumed session's plan must still be at the path that session is writing to")
	assert.Equal(t, "still being written", string(body))
	assert.NoFileExists(t, filepath.Join(root, resumed, paths.PersistDirName, "wip"+paths.PlanFileExt),
		"and no half-copy under persist/ for the session to diverge from")

	assert.NoFileExists(t, endedPlan)
	assert.FileExists(t, filepath.Join(root, ended, paths.PersistDirName, "done"+paths.PlanFileExt))
}

// TestMigrateHarpArtifacts_LeavesALocklessHarpAlone pins the accepted cost of
// asking the lock: a harp with NO lock file is Indeterminate, and
// Indeterminate refuses even a rename. "No lock file" is not only a session
// from before the lock existed — a session whose Hold FAILED keeps running
// without one by design (AssignSessionHarp), so a lock-less harp can be
// a plan somebody is writing right now, and the fork hazard is exactly as
// real as for a held lock. Its file waits until the session is run and ended
// under the lock; the index's ended_at does not stand in for the proof.
//
// MUTATION: treat a missing lock as permission to migrate → red.
func TestMigrateHarpArtifacts_LeavesALocklessHarpAlone(t *testing.T) {
	root := isolatedSessionsRoot(t)
	projectDir := t.TempDir()

	lockless := locklessEndedSession(t, projectDir)
	plan := writeHarpFile(t, root, lockless, "design"+paths.PlanFileExt, "nobody can prove this is not being written")

	got, err := MigrateHarpArtifacts(root)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{RefusedHarps: 1}, got)

	assert.FileExists(t, plan, "an ended_at with no lock file proves nothing; the file stays put")
	assert.NoFileExists(t, filepath.Join(root, lockless, paths.PersistDirName, "design"+paths.PlanFileExt))
}

// TestMigrateHarpArtifacts_NeverOverwrites: two different documents that share
// a name. Clobbering one with the other would destroy authored work and report
// a successful migration while doing it, so both must survive untouched.
func TestMigrateHarpArtifacts_NeverOverwrites(t *testing.T) {
	root := isolatedSessionsRoot(t)
	const harp = "brisk-teal-otter"
	src := writeHarpFile(t, root, harp, "design"+paths.PlanFileExt, "the top-level copy")
	dst := filepath.Join(root, harp, paths.PersistDirName, "design"+paths.PlanFileExt)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, []byte("the persist copy"), 0o644))
	endedUnderLock(t, harp)

	got, err := MigrateHarpArtifacts(root)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{Skipped: 1}, got)

	kept, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "the persist copy", string(kept), "the durable copy must not be replaced by the top-level one")
	still, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, "the top-level copy", string(still), "and the top-level one must not be destroyed either")
}

// TestMigrateHarpArtifacts_LeavesIrregularEntriesAlone: a symlink moved one
// directory deeper silently breaks when its target is relative, and ctxloom's
// own retired agent-bus.sock sits at the harp top level on real machines. Both
// are excluded outright rather than skipped, so the tally reports the honest
// number and doctor does not warn about a file nothing can move.
func TestMigrateHarpArtifacts_LeavesIrregularEntriesAlone(t *testing.T) {
	testsupport.Isolate(t)
	// The harp root hosts a bound unix socket, so it comes from SocketDir
	// rather than a temp root. The lock resolves from the isolated home
	// regardless of where the harp directory sits.
	const harp = "brisk-teal-otter"
	root := testsupport.SocketDir(t, filepath.Join(harp, "agent-bus.sock"))
	target := writeHarpFile(t, root, harp, "real.md", "body")
	link := filepath.Join(root, harp, "pointer"+paths.PlanFileExt)
	require.NoError(t, os.Symlink(filepath.Base(target), link))
	sock := filepath.Join(root, harp, "agent-bus.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	endedUnderLock(t, harp)

	names, err := HarpTopLevelArtifacts(filepath.Join(root, harp))
	require.NoError(t, err)
	assert.Equal(t, []string{"real.md"}, names,
		"neither the link nor the socket is reported as authored work at risk — a warning with no action behind it is one a user learns to ignore")

	got, err := MigrateHarpArtifacts(root)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{Moved: 1}, got, "real.md moves; nothing else is even a candidate")

	info, err := os.Lstat(link)
	require.NoError(t, err, "the symlink must still be at the harp top level")
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	assert.NoFileExists(t, filepath.Join(root, harp, paths.PersistDirName, "pointer"+paths.PlanFileExt))

	_, err = os.Lstat(sock)
	assert.NoError(t, err, "the socket stays where the process that bound it expects it")
	assert.NoFileExists(t, filepath.Join(root, harp, paths.PersistDirName, "agent-bus.sock"))

	after, err := HarpTopLevelArtifacts(filepath.Join(root, harp))
	require.NoError(t, err)
	assert.Empty(t, after, "and after the sweep nothing is left for doctor to warn about — the warning is CLEARABLE")
}

// TestMigrateHarpArtifacts_MissingRootIsNotAFault: a machine that has never
// run a session has nothing to migrate, and startup must not warn about it.
func TestMigrateHarpArtifacts_MissingRootIsNotAFault(t *testing.T) {
	got, err := MigrateHarpArtifacts(filepath.Join(t.TempDir(), "never-existed"))
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{}, got)
}
