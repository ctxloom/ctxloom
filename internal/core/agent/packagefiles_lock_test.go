package agent

import (
	"testing"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// lockIsFree reports whether nobody holds the advisory lock guarding target,
// probing it as a second process would — a separate open file description and
// a non-blocking flock — so the adversarial scheduler below never waits.
func lockIsFree(t *testing.T, target string) bool {
	t.Helper()
	lockPath, err := paths.HomePathFor(target)
	require.NoError(t, err)
	require.NoError(t, filelock.Prepare(lockPath))
	probe := flock.New(lockPath)
	free, err := probe.TryLock()
	require.NoError(t, err)
	if free {
		require.NoError(t, probe.Unlock())
	}
	return free
}

// TestWriteManagedPackageFiles_ExcludesAConcurrentWriterOfItsDir forces the
// lost update a writer of one managed dir suffers when its cycle is not
// serialized: writer A reads the surface's previous set, and before A records
// its own set writer B — the same surface, the same dir, another session or
// profile — completes a whole delivery. A then writes a ledger built from its
// stale read: B's files are still on disk but no surface claims them, so no
// later cleanup will ever remove them.
//
// Forced, not waited for: A's render runs inside its cycle, and the scheduler
// slips B in there whenever the dir's lock lets a second writer through.
func TestWriteManagedPackageFiles_ExcludesAConcurrentWriterOfItsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	dir := t.TempDir()

	item := func(name string) []fakeSkillItem {
		return []fakeSkillItem{{name: name, enabled: true,
			files: []PackageFile{{RelPath: name + "/SKILL.md", Content: []byte(name)}}}}
	}
	write := func(items []fakeSkillItem, render func(fakeSkillItem) ([]PackageFile, error)) error {
		return WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, render,
			WithWriteReporter(termRep().Sink))
	}
	writeB := func() error { return write(item("b"), skillRender) }

	bInWindow := false
	renderA := func(i fakeSkillItem) ([]PackageFile, error) {
		if lockIsFree(t, dir) {
			bInWindow = true
			require.NoError(t, writeB())
		}
		return skillRender(i)
	}
	require.NoError(t, write(item("a"), renderA))
	if !bInWindow {
		require.NoError(t, writeB())
	}

	claimed, err := ledger.Ledger{FS: fs, Dir: dir}.Read(ledger.SurfaceSkills)
	require.NoError(t, err)
	onDisk := map[string]bool{}
	for _, name := range []string{"a", "b"} {
		if ok, _ := afero.Exists(fs, dir+"/"+name+"/SKILL.md"); ok {
			onDisk[name+"/SKILL.md"] = true
		}
	}
	assert.False(t, bInWindow, "a second writer got inside another's cycle over the same managed dir")
	assert.Equal(t, []string{"b/SKILL.md"}, claimed, "the last writer's set must be what the ledger claims")
	assert.Equal(t, map[string]bool{"b/SKILL.md": true}, onDisk,
		"every managed file on disk must be one the ledger claims; anything else is orphaned for good")
}
