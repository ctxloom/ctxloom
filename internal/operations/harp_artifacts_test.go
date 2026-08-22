package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

func writeHarpFile(t *testing.T, root, harp, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, harp)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
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
	root := t.TempDir()
	const harp = "brisk-teal-otter"
	const body = "# design\n\nthe decision and why\n"
	src := writeHarpFile(t, root, harp, "design"+paths.PlanFileExt, body)
	writeHarpFile(t, root, harp, paths.EssenceFileName, "ctxloom's own")

	got, err := MigrateHarpArtifacts(root, nil)
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

// TestMigrateHarpArtifacts_SkipsLiveSessions: a running agent holds the old
// path and will write to it again. Moving the file mid-session does not
// relocate the plan, it forks it — half under persist/, the rest recreated at
// the top level by the next edit, and neither copy complete.
func TestMigrateHarpArtifacts_SkipsLiveSessions(t *testing.T) {
	root := t.TempDir()
	live := writeHarpFile(t, root, "live-harp", "wip"+paths.PlanFileExt, "still being written")
	ended := writeHarpFile(t, root, "ended-harp", "done"+paths.PlanFileExt, "finished")

	got, err := MigrateHarpArtifacts(root, map[string]bool{"live-harp": true})
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{Moved: 1, LiveHarps: 1}, got)

	body, err := os.ReadFile(live)
	require.NoError(t, err, "the live session's plan must still be at the path that session is writing to")
	assert.Equal(t, "still being written", string(body))
	assert.NoFileExists(t, filepath.Join(root, "live-harp", paths.PersistDirName, "wip"+paths.PlanFileExt),
		"and no half-copy under persist/ for the session to diverge from")

	assert.NoFileExists(t, ended)
	assert.FileExists(t, filepath.Join(root, "ended-harp", paths.PersistDirName, "done"+paths.PlanFileExt))
}

// TestMigrateHarpArtifacts_NeverOverwrites: two different documents that share
// a name. Clobbering one with the other would destroy authored work and report
// a successful migration while doing it, so both must survive untouched.
func TestMigrateHarpArtifacts_NeverOverwrites(t *testing.T) {
	root := t.TempDir()
	const harp = "brisk-teal-otter"
	src := writeHarpFile(t, root, harp, "design"+paths.PlanFileExt, "the top-level copy")
	dst := filepath.Join(root, harp, paths.PersistDirName, "design"+paths.PlanFileExt)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, []byte("the persist copy"), 0o644))

	got, err := MigrateHarpArtifacts(root, nil)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{Skipped: 1}, got)

	kept, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "the persist copy", string(kept), "the durable copy must not be replaced by the top-level one")
	still, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, "the top-level copy", string(still), "and the top-level one must not be destroyed either")
}

// TestMigrateHarpArtifacts_LeavesSymlinksAlone: moving a symlink one directory
// deeper silently breaks it when its target is relative, and this sweep has no
// business rewriting link targets.
func TestMigrateHarpArtifacts_LeavesSymlinksAlone(t *testing.T) {
	root := t.TempDir()
	const harp = "brisk-teal-otter"
	target := writeHarpFile(t, root, harp, "real.md", "body")
	link := filepath.Join(root, harp, "pointer"+paths.PlanFileExt)
	require.NoError(t, os.Symlink(filepath.Base(target), link))

	got, err := MigrateHarpArtifacts(root, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Moved, "real.md is a regular file and moves")
	assert.Equal(t, 1, got.Skipped, "the symlink does not")

	info, err := os.Lstat(link)
	require.NoError(t, err, "the symlink must still be at the harp top level")
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	assert.NoFileExists(t, filepath.Join(root, harp, paths.PersistDirName, "pointer"+paths.PlanFileExt))
}

// TestMigrateHarpArtifacts_MissingRootIsNotAFault: a machine that has never
// run a session has nothing to migrate, and startup must not warn about it.
func TestMigrateHarpArtifacts_MissingRootIsNotAFault(t *testing.T) {
	got, err := MigrateHarpArtifacts(filepath.Join(t.TempDir(), "never-existed"), nil)
	require.NoError(t, err)
	assert.Equal(t, HarpArtifactMigration{}, got)
}
