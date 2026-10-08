package isolation

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// memLinker is the platform's directory links over an in-memory filesystem:
// a link is a file at its path whose target the linker remembers. It counts
// the links it makes, so a test can tell a kept link from a remade one.
type memLinker struct {
	t     testing.TB
	fs    afero.Fs
	links map[string]string
	made  int
}

func newMemLinker(t testing.TB, fsys afero.Fs) *memLinker {
	return &memLinker{t: t, fs: fsys, links: map[string]string{}}
}

func (l *memLinker) LinkDir(link, target string) error {
	if _, err := l.fs.Stat(link); err == nil {
		return fs.ErrExist
	}
	testsupport.WriteFileString(l.t, l.fs, link, target, 0o600)
	l.links[link] = target
	l.made++
	return nil
}

func (l *memLinker) LinkTarget(link string) (string, error) {
	t, ok := l.links[link]
	if !ok {
		return "", errors.New("not a link")
	}
	return t, nil
}

func (l *memLinker) UnlinkDir(link string) error {
	if _, ok := l.links[link]; !ok {
		return errors.New("not a link")
	}
	delete(l.links, link)
	return l.fs.Remove(link)
}

// memMerge is one merged tree on an in-memory filesystem: the user's base
// holding two app dirs, a file, and a dir the engine owns.
type memMerge struct {
	fs   afero.Fs
	root safefs.Root
	ln   *memLinker
	b    xdgBase
}

func newMemMerge(t *testing.T) *memMerge {
	t.Helper()
	fsys := afero.NewMemMapFs()
	m := &memMerge{fs: fsys, root: safefs.NewMem(fsys), ln: newMemLinker(t, fsys), b: xdgBase{
		name:  "XDG_CONFIG_HOME",
		dir:   filepath.FromSlash("/s/home/eng/.xdg/config"),
		user:  filepath.FromSlash("/u/.config"),
		owns:  []string{"eng"},
		files: true,
	}}
	for _, d := range []string{"gh", "git", "eng"} {
		require.NoError(t, fsys.MkdirAll(filepath.Join(m.b.user, d), 0o755))
	}
	testsupport.WriteFile(t, fsys, filepath.Join(m.b.user, "mimeapps.list"), nil, 0o644)
	require.NoError(t, fsys.MkdirAll(m.b.dir, 0o700))
	return m
}

func (m *memMerge) run(t *testing.T) []string {
	t.Helper()
	skipped, err := mergeBase(m.root, m.ln, m.b)
	require.NoError(t, err)
	return skipped
}

// entries is the tree's top level: each name, and the user entry it links
// to ("" for the session's own).
func (m *memMerge) entries(t *testing.T) map[string]string {
	t.Helper()
	infos, err := afero.ReadDir(m.fs, m.b.dir)
	require.NoError(t, err)
	out := map[string]string{}
	for _, fi := range infos {
		out[fi.Name()] = m.ln.links[filepath.Join(m.b.dir, fi.Name())]
	}
	return out
}

// THE HOST MERGE: every top-level entry of the user's base is linked in,
// file or dir, except an owned name, which is the session's own directory,
// owner-only, and never the user's.
func TestMergeBase_HostLinksEveryUserEntryAndShadowsTheOwned(t *testing.T) {
	m := newMemMerge(t)
	assert.Empty(t, m.run(t))
	u := m.b.user
	assert.Equal(t, map[string]string{
		"gh":            filepath.Join(u, "gh"),
		"git":           filepath.Join(u, "git"),
		"mimeapps.list": filepath.Join(u, "mimeapps.list"),
		"eng":           "",
	}, m.entries(t))
	fi, err := m.fs.Stat(filepath.Join(m.b.dir, "eng"))
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "an owned dir is owner-only")
}

// THE CONTAINER PARITY: a run given no user base holds the owned dirs alone,
// and the links an earlier host run of the same session left are removed, so
// a container does not even see the names of the user's entries.
func TestMergeBase_AContainerHoldsTheOwnedDirsAloneAndDropsHostLinks(t *testing.T) {
	m := newMemMerge(t)
	m.run(t)
	m.b.user = ""
	assert.Empty(t, m.run(t))
	assert.Equal(t, map[string]string{"eng": ""}, m.entries(t))
	_, err := m.fs.Stat(filepath.Join(filepath.FromSlash("/u/.config"), "gh"))
	assert.NoError(t, err, "unlinking never reaches the user's entry")
}

// A later run re-snapshots: a correct link stays as it is (a running process
// never loses it), an entry the user removed loses its link, a new one gains
// one, and a link elsewhere at a user's name is replaced.
func TestMergeBase_ALaterRunReconcilesToTheNewSnapshot(t *testing.T) {
	m := newMemMerge(t)
	m.run(t)
	require.NoError(t, m.fs.RemoveAll(filepath.Join(m.b.user, "git")))
	require.NoError(t, m.fs.MkdirAll(filepath.Join(m.b.user, "fish"), 0o755))
	ghLink := filepath.Join(m.b.dir, "gh")
	require.NoError(t, m.ln.UnlinkDir(filepath.Join(m.b.dir, "mimeapps.list")))
	require.NoError(t, m.ln.LinkDir(filepath.Join(m.b.dir, "mimeapps.list"), filepath.FromSlash("/old/base/mimeapps.list")))
	m.ln.made = 0

	assert.Empty(t, m.run(t))
	u := m.b.user
	assert.Equal(t, map[string]string{
		"gh":            filepath.Join(u, "gh"),
		"fish":          filepath.Join(u, "fish"),
		"mimeapps.list": filepath.Join(u, "mimeapps.list"),
		"eng":           "",
	}, m.entries(t))
	assert.Equal(t, 2, m.ln.made, "only fish and the re-pointed mimeapps.list are linked; gh's link is kept")
	assert.Equal(t, filepath.Join(u, "gh"), m.ln.links[ghLink])
}

// A name the session already holds for itself (a tool created it in the
// session's tree) is kept, and the user's entry of that name is skipped with
// a report.
func TestMergeBase_TheSessionsOwnEntryIsKeptAndTheUsersSkipped(t *testing.T) {
	m := newMemMerge(t)
	require.NoError(t, m.fs.MkdirAll(filepath.Join(m.b.dir, "gh"), 0o700))
	skipped := m.run(t)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0], filepath.Join(m.b.user, "gh"))
	assert.Equal(t, "", m.entries(t)["gh"])
}

// A name that became owned after an earlier run linked it: the link is
// removed (never followed: the user's dir is not restricted) and the name
// becomes the session's own directory.
func TestMergeBase_ANewlyOwnedNameReplacesItsLinkWithTheSessionsDir(t *testing.T) {
	m := newMemMerge(t)
	m.b.owns = nil
	m.run(t)
	require.Equal(t, filepath.Join(m.b.user, "eng"), m.entries(t)["eng"])
	m.b.owns = []string{"eng"}
	m.run(t)
	assert.Equal(t, "", m.entries(t)["eng"])
	fi, err := m.fs.Stat(filepath.Join(m.b.user, "eng"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm(), "the user's dir is untouched")
}

// A missing user base is skipped with a report; the owned dirs are still made.
func TestMergeBase_AMissingUserBaseIsSkippedWithAReport(t *testing.T) {
	m := newMemMerge(t)
	m.b.user = filepath.FromSlash("/nobody/.config")
	skipped := m.run(t)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0], "does not exist")
	assert.Equal(t, map[string]string{"eng": ""}, m.entries(t))
}

// Where the platform's links join directories only (a Windows junction), a
// file entry is skipped with a report and the directories are still linked.
func TestMergeBase_AFileIsSkippedWhereLinksJoinDirectoriesOnly(t *testing.T) {
	m := newMemMerge(t)
	m.b.files = false
	skipped := m.run(t)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0], "mimeapps.list")
	assert.NotContains(t, m.entries(t), "mimeapps.list")
	assert.Contains(t, m.entries(t), "gh")
}

// A user base that is, or holds, the session's own tree is never linked from:
// a nested ctxloom run whose $XDG_CONFIG_HOME names the tree it is building.
func TestMergeBase_AUserBaseOverlappingTheTreeIsSkipped(t *testing.T) {
	for name, user := range map[string]string{"the tree": "/s/home/eng/.xdg/config", "above it": "/s/home"} {
		t.Run(name, func(t *testing.T) {
			m := newMemMerge(t)
			m.b.user = filepath.FromSlash(user)
			skipped := m.run(t)
			require.Len(t, skipped, 1)
			assert.Contains(t, skipped[0], "overlaps")
			assert.Equal(t, map[string]string{"eng": ""}, m.entries(t))
		})
	}
}

// mergedEngine is a fixture engine whose XDG config base is a merged tree
// owning one app dir, beside a plain further var.
func mergedEngine(t *testing.T) engine.Engine {
	t.Helper()
	eng := mock.NewNamed("xdgmerge-fixture", mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{
			{Name: "FIXTURE_HOME", Subdir: "fixture"},
			{Name: "XDG_CONFIG_HOME", Subdir: ".xdg/config", Merge: &engine.XDGMerge{Owns: []string{"fixtureapp"}}},
			{Name: "FIXTURE_PLAIN", Subdir: "plain"},
		},
		Auth: engine.Absent[engine.Auth]("the fixture authenticates against no vendor"),
	}))
	require.NoError(t, eng.Home().Validate())
	stageEngineFacts(t, string(eng.Root().Name), func(f *EngineFacts) { *f = FactsOf(eng) })
	return eng
}

// userConfig makes a user XDG config base under the test's temp home with
// one app dir, one file and the fixture's owned name, and points
// $XDG_CONFIG_HOME at it (set) or leaves the spec default to find it.
func userConfig(t *testing.T, home string, set bool) string {
	t.Helper()
	base := filepath.Join(home, ".config")
	if set {
		base = filepath.Join(home, "elsewhere-config")
		t.Setenv("XDG_CONFIG_HOME", base)
	} else {
		t.Setenv("XDG_CONFIG_HOME", "")
	}
	for _, d := range []string{"tool", "fixtureapp"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "tool", "config"), []byte("v1"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "list"), []byte("l"), 0o600))
	return base
}

// END TO END ON THE HOST, through the real relocator and the platform's
// links: the engine's XDG_CONFIG_HOME names the session's tree, which shows
// the user's entries live (resolved per the spec, set or default) and the
// engine's own dir for the owned name.
func TestXDGMerge_AHostRunSeesTheUsersBaseWithTheOwnedDirShadowed(t *testing.T) {
	for name, set := range map[string]bool{"var set": true, "spec default": false} {
		t.Run(name, func(t *testing.T) {
			home := fakeHostHome(t, "")
			base := userConfig(t, home, set)
			s := homeSpec(t, mergedEngine(t), home, harpA, agents.HomeModeSession)

			pl, _ := placeOn(t, s, t.TempDir(), hostRelocator{})
			tree := pl.Env["XDG_CONFIG_HOME"]
			require.NotEmpty(t, tree)
			assert.True(t, within(tree, pl.Paths.Paths().SessionHome.Host), "the tree is the session's")

			got, err := os.ReadFile(filepath.Join(tree, "tool", "config"))
			require.NoError(t, err)
			assert.Equal(t, "v1", string(got))
			require.NoError(t, os.WriteFile(filepath.Join(base, "tool", "config"), []byte("v2"), 0o600))
			got, err = os.ReadFile(filepath.Join(tree, "tool", "config"))
			require.NoError(t, err)
			assert.Equal(t, "v2", string(got), "contents are live")

			own := filepath.Join(tree, "fixtureapp")
			linked, err := hostOS.LinksTo(own, filepath.Join(base, "fixtureapp"))
			require.NoError(t, err)
			assert.False(t, linked, "the owned name is the session's own")
			fi, err := os.Lstat(own)
			require.NoError(t, err)
			assert.True(t, fi.IsDir())
			if runtime.GOOS == "windows" {
				assert.NotContains(t, homeEntries(t, tree), "list", "a junction joins directories only")
			} else {
				assert.Equal(t, []string{"fixtureapp", "list", "tool"}, homeEntries(t, tree))
			}
			assert.Empty(t, strictness.All())
		})
	}
}

// END TO END IN A CONTAINER (the mount plan; no daemon): the tree holds the
// owned dir alone, it rides the session home's one mount, and no mount
// sources anything under the user's base — a container is given none of the
// user's XDG content, as no container run is today.
func TestXDGMerge_AContainerRunIsGivenNoneOfTheUsersBase(t *testing.T) {
	home := fakeHostHome(t, "")
	base := userConfig(t, home, true)
	s := homeSpec(t, mergedEngine(t), home, harpA, agents.HomeModeSession)

	pl, mounts := placeOn(t, s, t.TempDir(), containerOf)
	sh := pl.Paths.Paths().SessionHome
	tree := filepath.Join(sh.Host, ".xdg", "config")
	assert.Equal(t, []string{"fixtureapp"}, homeEntries(t, tree))
	for _, m := range mounts {
		assert.False(t, within(m.Host, base), "no mount sources the user's base: %s", m.Host)
		assert.NotContains(t, m.Container, ".xdg", "the tree rides the home's mount")
	}
	assert.Equal(t, sh.Engine+"/.xdg/config", pl.Env["XDG_CONFIG_HOME"])
	assert.Empty(t, strictness.All())
}

// A container run after a host run of the same session: the host's links
// are gone from the shared session home before the container starts.
func TestXDGMerge_AContainerRunAfterAHostRunDropsTheHostsLinks(t *testing.T) {
	home := fakeHostHome(t, "")
	userConfig(t, home, true)
	s := homeSpec(t, mergedEngine(t), home, harpA, agents.HomeModeSession)

	pl, _ := placeOn(t, s, t.TempDir(), hostRelocator{})
	tree := pl.Env["XDG_CONFIG_HOME"]
	require.True(t, slices.Contains(homeEntries(t, tree), "tool"))
	placeOn(t, s, t.TempDir(), containerOf)
	assert.Equal(t, []string{"fixtureapp"}, homeEntries(t, tree))
}

// CHARACTERIZATION: claude declares no merged base, so nothing of the user's
// XDG content reaches its session home on either runtime.
func TestXDGMerge_ClaudeMergesNothing(t *testing.T) {
	for _, v := range claudeEngine(t).Home().Vars {
		assert.Nil(t, v.Merge, v.Name)
	}
}
