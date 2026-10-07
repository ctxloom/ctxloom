package agent

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// lockIsFree reports whether nobody holds the advisory lock guarding target,
// probing it as a second process would — a separate open file description and
// a non-blocking flock — so the adversarial scheduler below never waits.
func lockIsFree(t *testing.T, target string) bool {
	t.Helper()
	lockPath, err := paths.HomePathFor(target)
	require.NoError(t, err)
	held, err := safefs.New().Locks.Held(lockPath)
	require.NoError(t, err)
	return !held
}

// TestWriteManagedPackageFiles_ExcludesAConcurrentWriterOfItsDir: a writer
// holds its dir's lock across its whole cycle, from the first render to the
// last swap, so a second writer of the same dir — another session or profile
// — cannot complete inside it.
//
// Forced, not waited for: A's render runs inside its cycle, and the scheduler
// slips B in there whenever the dir's lock lets a second writer through.
func TestWriteManagedPackageFiles_ExcludesAConcurrentWriterOfItsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Production shape: the static writer runs every approach over a
	// copy-on-write overlay of the controller's filesystem, paired with the
	// controller's real locks — never over the OS fs itself.
	fs := afero.NewCopyOnWriteFs(afero.NewOsFs(), afero.NewMemMapFs())
	files := safefs.Root{Fs: fs, Locks: safefs.New().Locks}
	dir := t.TempDir()

	item := func(name string) []fakeSkillItem {
		return []fakeSkillItem{{name: name, enabled: true,
			files: []PackageFile{{RelPath: name + "/SKILL.md", Content: []byte(name)}}}}
	}
	write := func(items []fakeSkillItem, render func(fakeSkillItem) ([]PackageFile, error)) error {
		_, err := WriteManagedPackageFiles(files, dir, items, skillEnabled, skillName, render,
			WithWriteReporter(termRep().Sink))
		return err
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
	require.False(t, bInWindow, "a second writer got inside another's cycle over the same managed dir")
}
