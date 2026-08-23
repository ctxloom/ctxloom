package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectPathFor_LandsUnderStateLocks pins the shape: a lock guarding a
// file in a project .ctxloom tree lives under that tree's state/locks, not
// beside the file it protects. Beside-the-file left `.ctxloom/config.yaml.lock`
// sitting at the root of a freshly initialized project, untracked and — until
// the pattern added alongside this change — unignored.
func TestProjectPathFor_LandsUnderStateLocks(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, AppDirName)
	protected := filepath.Join(appDir, "config.yaml")

	got, err := ProjectPathFor(protected)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(appDir, StateDir, LocksDir, "config.yaml.lock"), got)
	assert.NotEqual(t, PathFor(protected), got,
		"the project mapping must not collapse back to the beside-the-file shape")
}

// TestProjectPathFor_FlattensNestedPaths pins that a protected file below the
// .ctxloom root keeps its whole relative path in the lock's NAME. Two files
// that differ only in their directory must not share a lock by accident of
// having the same basename.
func TestProjectPathFor_FlattensNestedPaths(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, AppDirName)

	nested, err := ProjectPathFor(filepath.Join(appDir, "content", "bundles", "x.yaml"))
	require.NoError(t, err)
	root, err := ProjectPathFor(filepath.Join(appDir, "x.yaml"))
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(appDir, StateDir, LocksDir), filepath.Dir(nested),
		"flattening must keep every lock in ONE directory; a lock tree that mirrors the project tree is a second convention")
	assert.NotEqual(t, root, nested,
		"two distinct protected files must not be handed the same lock by basename collision")
}

// TestProjectPathFor_SpellingsOfOneFileMapToOneLock is THE test this mapping
// exists for. lockSuffix's own doc (lockpath.go) calls one-name-per-resource
// this file's most breakable invariant: two writers of the same file that
// name its lock differently do not
// exclude each other, and nothing reports it — no error, no warning, just two
// writers where there was meant to be one. A mapping that goes through a
// relative path is exactly where that breaks, so every spelling a caller can
// plausibly hold of the same file must land on one lock path.
func TestProjectPathFor_SpellingsOfOneFileMapToOneLock(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, AppDirName)
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "content"), 0o755))

	canonical := filepath.Join(appDir, "config.yaml")
	spellings := map[string]string{
		"canonical":          canonical,
		"dot segment":        filepath.Join(appDir, ".", "config.yaml"),
		"dot-dot round trip": filepath.Join(appDir, "content", "..", "config.yaml"),
		"unclean separators": appDir + string(filepath.Separator) + string(filepath.Separator) + "config.yaml",
	}

	want, err := ProjectPathFor(canonical)
	require.NoError(t, err)
	for name, spelling := range spellings {
		t.Run(name, func(t *testing.T) {
			got, err := ProjectPathFor(spelling)
			require.NoError(t, err)
			assert.Equal(t, want, got,
				"%q and %q name the same file; two lock paths means two writers that do not exclude each other", spelling, canonical)
		})
	}
}

// TestProjectPathFor_RelativeSpellingMapsToTheSameLock covers the spelling a
// caller gets from a relative appDir resolution rather than an absolute one:
// `.ctxloom/config.yaml` read from the project root is the same file as its
// absolute form, and must not get a second lock.
func TestProjectPathFor_RelativeSpellingMapsToTheSameLock(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	// Chdir is scoped to this test by t.Chdir, which restores it on cleanup.
	t.Chdir(dir)

	fromAbs, err := ProjectPathFor(filepath.Join(appDir, "config.yaml"))
	require.NoError(t, err)
	fromRel, err := ProjectPathFor(filepath.Join(AppDirName, "config.yaml"))
	require.NoError(t, err)

	assert.Equal(t, fromAbs, fromRel,
		"a relative and an absolute spelling of one file must name one lock")
}

// TestProjectPathFor_RejectsAPathOutsideAnAppDir keeps the mapping honest about
// its own boundary. It is the PROJECT .ctxloom rule; a caller that hands it
// something else has made a mistake, and inventing a lock location for it would
// hide that under a lock nobody else takes.
func TestProjectPathFor_RejectsAPathOutsideAnAppDir(t *testing.T) {
	dir := t.TempDir()

	_, err := ProjectPathFor(filepath.Join(dir, "elsewhere", "index.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), AppDirName,
		"the error must name what it was looking for")
}
