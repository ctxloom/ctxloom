package ledger

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLedger(t *testing.T) (Ledger, afero.Fs, *[]string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/dir", 0o755))
	var warnings []string
	l := Ledger{
		FS:  fs,
		Dir: "/dir",
		Warn: func(format string, args ...any) {
			warnings = append(warnings, format)
		},
	}
	return l, fs, &warnings
}

// A ledger nobody has written yet is the legitimate "nothing managed here"
// case, not an error — every writer calls Read before its first Write.
func TestRead_MissingFile_ReturnsNothingAndNoError(t *testing.T) {
	l, _, _ := newLedger(t)

	names, err := l.Read(SurfaceMCP)

	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestWriteThenRead_RoundTripsTheNames(t *testing.T) {
	l, _, _ := newLedger(t)

	require.NoError(t, l.Write(SurfaceMCP, []string{"ctxloom", "taskloom"}))
	names, err := l.Read(SurfaceMCP)

	require.NoError(t, err)
	assert.Equal(t, []string{"ctxloom", "taskloom"}, names)
}

// THE REGRESSION THIS PACKAGE EXISTS TO PREVENT. kiro writes
// COMMANDS and SKILLS into the same directory, and before this type they
// relied on two separate manifest FILENAMES to stop one surface's cleanup
// deleting the other's content. One file with typed entries has to give the
// same guarantee, so writing either surface must leave the other intact.
func TestWrite_OneSurface_LeavesACoLocatedSurfaceIntact(t *testing.T) {
	l, _, _ := newLedger(t)
	require.NoError(t, l.Write(SurfaceCommands, []string{"review.md"}))
	require.NoError(t, l.Write(SurfaceSkills, []string{"lint/SKILL.md"}))

	// Rewrite commands to a different set entirely.
	require.NoError(t, l.Write(SurfaceCommands, []string{"deploy.md"}))

	skills, err := l.Read(SurfaceSkills)
	require.NoError(t, err)
	assert.Equal(t, []string{"lint/SKILL.md"}, skills,
		"rewriting the commands surface must not disturb the skills surface sharing this directory")

	cmds, err := l.Read(SurfaceCommands)
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy.md"}, cmds)
}

// Emptying one surface must not remove a file another surface is still using:
// the file goes only when every surface is empty.
func TestWrite_EmptyingOneSurface_KeepsTheFileForTheOther(t *testing.T) {
	l, fs, _ := newLedger(t)
	require.NoError(t, l.Write(SurfaceCommands, []string{"review.md"}))
	require.NoError(t, l.Write(SurfaceSkills, []string{"lint/SKILL.md"}))

	require.NoError(t, l.Write(SurfaceCommands, nil))

	exists, err := afero.Exists(fs, l.Path())
	require.NoError(t, err)
	assert.True(t, exists, "the marker must survive while another surface still has entries")

	skills, err := l.Read(SurfaceSkills)
	require.NoError(t, err)
	assert.Equal(t, []string{"lint/SKILL.md"}, skills)
}

func TestWrite_EmptyingTheLastSurface_RemovesTheMarker(t *testing.T) {
	l, fs, _ := newLedger(t)
	require.NoError(t, l.Write(SurfaceCommands, []string{"review.md"}))

	require.NoError(t, l.Write(SurfaceCommands, nil))

	exists, err := afero.Exists(fs, l.Path())
	require.NoError(t, err)
	assert.False(t, exists, "a marker claiming nothing is debris; it must be removed")
}

// An untyped line must never be adopted by whichever surface happens to read
// first — that would hand one surface deletion rights over another's files,
// which is the same data-loss shape as sharing a filename.
func TestRead_UntypedLine_IsSkippedAndWarned(t *testing.T) {
	l, fs, warnings := newLedger(t)
	require.NoError(t, afero.WriteFile(fs, l.Path(), []byte("orphan-with-no-surface\nreview.md\tcommands\n"), 0o644))

	cmds, err := l.Read(SurfaceCommands)

	require.NoError(t, err)
	assert.Equal(t, []string{"review.md"}, cmds, "the malformed line must not be adopted by this surface")
	assert.NotEmpty(t, *warnings, "a malformed line must be reported, not dropped in silence")

	skills, err := l.Read(SurfaceSkills)
	require.NoError(t, err)
	assert.Empty(t, skills, "nor by any other surface")
}

// A read error that is NOT "file absent" (permissions, I/O) must surface
// rather than be flattened into the legitimate empty case — flattening it is
// how a writer would conclude it manages nothing and orphan every entry.
func TestRead_UnreadableFile_ReturnsTheError(t *testing.T) {
	l, fs, _ := newLedger(t)
	require.NoError(t, afero.WriteFile(fs, l.Path(), []byte("x\tmcp\n"), 0o644))
	l.FS = &failingFs{Fs: fs}

	_, err := l.Read(SurfaceMCP)

	assert.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist,
		"an unreadable ledger must not be reported as the legitimate absent case")
}

// Identifiers are data — they can originate in remote bundle content — so a
// name carrying the field separator must not be able to forge a surface.
func TestWrite_NameContainingATab_IsRejected(t *testing.T) {
	l, _, _ := newLedger(t)

	err := l.Write(SurfaceCommands, []string{"evil\tskills"})

	assert.Error(t, err, "a name that could forge a surface field must be refused at write")
}

// THE TYPE IS OPEN. A plugin or a future engine must be able to declare a
// surface this package has never heard of and use it with no registration and
// no change here — if this ever fails, someone has closed the type against a
// known set and every third-party surface silently stopped recording.
func TestWriteThenRead_ASurfaceThisPackageNeverDeclared_Works(t *testing.T) {
	l, _, _ := newLedger(t)
	const plugin = Surface("acme.linter")

	require.NoError(t, l.Write(plugin, []string{"rules/house.json"}))
	got, err := l.Read(plugin)

	require.NoError(t, err)
	assert.Equal(t, []string{"rules/house.json"}, got)
}

// And an unknown surface must be isolated from the built-ins the same way two
// built-ins are isolated from each other — otherwise a plugin sharing a
// directory with ctxloom's own writers would lose its entries on the next
// materialize.
func TestWrite_ABuiltinSurface_LeavesAPluginSurfaceIntact(t *testing.T) {
	l, _, _ := newLedger(t)
	const plugin = Surface("acme.linter")
	require.NoError(t, l.Write(plugin, []string{"rules/house.json"}))
	require.NoError(t, l.Write(SurfaceSkills, []string{"lint/SKILL.md"}))

	require.NoError(t, l.Write(SurfaceSkills, []string{"other/SKILL.md"}))

	got, err := l.Read(plugin)
	require.NoError(t, err)
	assert.Equal(t, []string{"rules/house.json"}, got,
		"a built-in surface's cleanup must not reach a plugin's entries")
}

// Open does NOT mean unvalidated: a surface carrying the field separator could
// forge a second field and claim another surface's entries.
func TestWrite_SurfaceWithASeparatorOrEmpty_IsRejected(t *testing.T) {
	for _, s := range []Surface{"evil\tskills", "", "  ", "two\nlines"} {
		l, _, _ := newLedger(t)
		err := l.Write(s, []string{"x"})
		assert.Error(t, err, "surface %q must be refused", s)
	}
}

func TestWrite_IsStableAcrossRuns(t *testing.T) {
	l, fs, _ := newLedger(t)

	require.NoError(t, l.Write(SurfaceMCP, []string{"taskloom", "ctxloom"}))
	first, err := afero.ReadFile(fs, l.Path())
	require.NoError(t, err)

	require.NoError(t, l.Write(SurfaceMCP, []string{"ctxloom", "taskloom"}))
	second, err := afero.ReadFile(fs, l.Path())
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second),
		"the same managed set must render identically, or every materialize churns the file")
}

// failingFs makes reads fail with something other than os.ErrNotExist, so the
// "absent means nothing managed" branch cannot swallow a real I/O failure.
type failingFs struct{ afero.Fs }

func (f *failingFs) Open(string) (afero.File, error) { return nil, assert.AnError }

func (f *failingFs) OpenFile(string, int, os.FileMode) (afero.File, error) {
	return nil, assert.AnError
}
