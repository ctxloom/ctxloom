package isolation

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
)

// junctionHost is a host whose directory links name absolute paths, as
// Windows' junctions do (platform/windows.OS): they do not resolve inside a
// container. Its links are absolute symbolic links, which stand in for a
// junction on any OS the test runs on.
type junctionHost struct{ platform.Host }

func (junctionHost) LinkDir(link, target string) error { return os.Symlink(target, link) }

func (junctionHost) LinksTo(link, target string) (bool, error) {
	if _, err := os.Lstat(link); err != nil {
		return false, err
	}
	got, err := os.Readlink(link)
	return err == nil && got == target, nil
}

func (junctionHost) UnlinkDir(link string) error { return os.Remove(link) }

func (junctionHost) LinkTarget(link string) (string, error) { return os.Readlink(link) }

func (junctionHost) LinksResolveInContainers() bool { return false }

// withHostOS stands h in for the platform for one test.
func withHostOS(t *testing.T, h platform.Host) {
	t.Helper()
	prev := hostOS
	hostOS = h
	t.Cleanup(func() { hostOS = prev })
}

// A container run on a host whose links cannot cross into the container keeps
// its history inside the mounted home: no link is made, native/ is not
// mounted, and the run goes ahead.
func TestNativeHistory_ContainerOnAnUnlinkableHostKeepsHistoryInTheHome(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

	_, mounts := placeOn(t, s, t.TempDir(), containerOf)

	for _, m := range mounts {
		assert.False(t, strings.Contains(m.Host, string(filepath.Separator)+paths.NativeDirName+string(filepath.Separator)),
			"native/ is not mounted: %s", m.Host)
	}
	_, err := os.Lstat(filepath.Join(claudeHome(home, harpA), claude.TranscriptsDirName))
	assert.ErrorIs(t, err, fs.ErrNotExist, "no link: the engine makes its history dir in the home itself")
}

// The same host still links a HOST run's history into native/.
func TestNativeHistory_HostRunOnAnUnlinkableHostStillLinks(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

	placeOn(t, s, t.TempDir(), hostRelocator{})

	st, err := os.Lstat(filepath.Join(claudeHome(home, harpA), claude.TranscriptsDirName))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode()&fs.ModeSymlink)
}

// history is the claude history store under a session home or native home.
func history(dir string) string { return filepath.Join(dir, claude.TranscriptsDirName) }

// writeHistory writes one transcript file under the history store at dir.
func writeHistory(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(history(dir), filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
}

// readHistory is one transcript file's content under the history store at dir.
func readHistory(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(history(dir), filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

// nativeOf is harp's native claude home under home (launch.NativeHome).
func nativeOf(home, harp string) string {
	return filepath.Join(sessionDir(home, harp), paths.NativeDirName, claude.HomeLeaf)
}

// Host then container on an unlinkable host: the container run cannot follow
// the junction, so the link is removed and the native history copied into
// the mounted home — the history the host run wrote is there to resume, and
// native/ still holds it.
func TestNativeHistory_HostThenContainerCopiesNativeHistoryIntoTheHome(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)
	placeOn(t, s, t.TempDir(), hostRelocator{})
	writeHistory(t, claudeHome(home, harpA), "-proj/s.jsonl", "host\n")

	placeOn(t, s, t.TempDir(), containerOf)

	st, err := os.Lstat(history(claudeHome(home, harpA)))
	require.NoError(t, err)
	assert.Equal(t, fs.ModeDir, st.Mode().Type(), "a real dir the container can write, not the junction")
	assert.Equal(t, "host\n", readHistory(t, claudeHome(home, harpA), "-proj/s.jsonl"))
	assert.Equal(t, "host\n", readHistory(t, nativeOf(home, harpA), "-proj/s.jsonl"), "copied, not moved")
}

// A container run in a home rebuilt after Close starts from the native
// history too.
func TestNativeHistory_ContainerInAFreshHomeStartsFromNativeHistory(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)
	writeHistory(t, nativeOf(home, harpA), "-proj/s.jsonl", "kept\n")

	placeOn(t, s, t.TempDir(), containerOf)

	assert.Equal(t, "kept\n", readHistory(t, claudeHome(home, harpA), "-proj/s.jsonl"))
}

// Container then host on an unlinkable host: the history the container run
// wrote into the home moves into native/ and the home is linked again. Where
// both hold a file the home's wins — it started as native's copy and only
// grew — and what only native/ holds is kept.
func TestNativeHistory_ContainerThenHostMovesHomeHistoryIntoNative(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)
	writeHistory(t, nativeOf(home, harpA), "-proj/s.jsonl", "old\n")
	writeHistory(t, nativeOf(home, harpA), "-other/o.jsonl", "native only\n")
	placeOn(t, s, t.TempDir(), containerOf)
	writeHistory(t, claudeHome(home, harpA), "-proj/s.jsonl", "old\ngrown\n")
	writeHistory(t, claudeHome(home, harpA), "-proj/new.jsonl", "container\n")

	placeOn(t, s, t.TempDir(), hostRelocator{})

	native := nativeOf(home, harpA)
	ok, err := hostOS.LinksTo(history(claudeHome(home, harpA)), history(native))
	require.NoError(t, err)
	assert.True(t, ok, "the home is linked into native/ again")
	assert.Equal(t, "old\ngrown\n", readHistory(t, native, "-proj/s.jsonl"))
	assert.Equal(t, "container\n", readHistory(t, native, "-proj/new.jsonl"))
	assert.Equal(t, "native only\n", readHistory(t, native, "-other/o.jsonl"))
}

// A renamed session's junction still names the native/ it had under its old
// name. The next host run links it to the native/ it has now, and the history
// that moved with the session is reachable again.
func TestNativeHistory_HostRunAfterARenameRelinksToTheMovedNative(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	home := fakeHostHome(t, tokenFixture)
	placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), hostRelocator{})
	writeHistory(t, claudeHome(home, harpA), "-proj/s.jsonl", "kept\n")
	require.NoError(t, os.Rename(sessionDir(home, harpA), sessionDir(home, harpB)))

	placeOn(t, homeSpec(t, claudeEngine(t), home, harpB, agents.HomeModeSession), t.TempDir(), hostRelocator{})

	ok, err := hostOS.LinksTo(history(claudeHome(home, harpB)), history(nativeOf(home, harpB)))
	require.NoError(t, err)
	assert.True(t, ok, "relinked to the session's native/ under its new name")
	assert.Equal(t, "kept\n", readHistory(t, claudeHome(home, harpB), "-proj/s.jsonl"))
}

// A junction into ANOTHER live session's native/ is not this session's old
// path: refused, never relinked.
func TestNativeHistory_AJunctionIntoAnotherSessionsNativeIsRefused(t *testing.T) {
	withHostOS(t, junctionHost{platform.Current()})
	root := t.TempDir()
	at := func(harp string, elem ...string) string {
		return filepath.Join(append([]string{root, harp}, elem...)...)
	}
	other := at(harpA, paths.NativeDirName, claude.HomeLeaf, claude.TranscriptsDirName)
	require.NoError(t, os.MkdirAll(other, 0o700))
	instance := at(harpB, paths.SessionEngineHomesDirName, claude.HomeLeaf)
	require.NoError(t, os.MkdirAll(instance, 0o700))
	require.NoError(t, os.Symlink(other, filepath.Join(instance, claude.TranscriptsDirName)))

	err := linkNativeHistory(instance, at(harpB, paths.NativeDirName, claude.HomeLeaf), claude.TranscriptsDirName)
	assert.ErrorIs(t, err, ErrHistoryNotLinked)
}
