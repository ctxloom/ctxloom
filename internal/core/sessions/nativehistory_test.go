package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// put writes body at dir/rel, creating its parents.
func put(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
}

// got is dir/rel's content.
func got(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

// A real history dir in an engine home moves into native/ — the home's file
// wins where both hold one, native's own files are kept — and the home's copy
// is gone, so deleting the home loses nothing.
func TestKeepHomeHistory_MovesARealHistoryDirIntoNative(t *testing.T) {
	sd := t.TempDir()
	home := filepath.Join(sd, paths.SessionEngineHomesDirName, "claude")
	native := filepath.Join(sd, paths.NativeDirName, "claude")
	put(t, native, "projects/-p/s.jsonl", "old\n")
	put(t, native, "projects/-q/o.jsonl", "native only\n")
	put(t, home, "projects/-p/s.jsonl", "old\ngrown\n")
	put(t, home, "projects/-p/new.jsonl", "container\n")
	put(t, home, "settings.json", "{}")

	require.NoError(t, KeepHomeHistory(afero.NewOsFs(), sd))

	assert.Equal(t, "old\ngrown\n", got(t, native, "projects/-p/s.jsonl"))
	assert.Equal(t, "container\n", got(t, native, "projects/-p/new.jsonl"))
	assert.Equal(t, "native only\n", got(t, native, "projects/-q/o.jsonl"))
	assert.NoDirExists(t, filepath.Join(home, "projects"))
	assert.FileExists(t, filepath.Join(home, "settings.json"), "only history stores move")
}

// A home whose history is the link into native/ has nothing to move; the
// link and native's history are left as they are.
func TestKeepHomeHistory_LeavesALinkedHistoryAlone(t *testing.T) {
	sd := t.TempDir()
	home := filepath.Join(sd, paths.SessionEngineHomesDirName, "claude")
	native := filepath.Join(sd, paths.NativeDirName, "claude")
	put(t, native, "projects/-p/s.jsonl", "kept\n")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(native, "projects"), filepath.Join(home, "projects")))

	require.NoError(t, KeepHomeHistory(afero.NewOsFs(), sd))

	assert.Equal(t, "kept\n", got(t, native, "projects/-p/s.jsonl"))
}

// A session with no native/ has no history store to keep.
func TestKeepHomeHistory_WithoutNativeIsANoOp(t *testing.T) {
	assert.NoError(t, KeepHomeHistory(afero.NewOsFs(), t.TempDir()))
}

// A NESTED history store (one below a further home var, e.g. an XDG data
// base's app dir) that is the link into native/ has nothing to move: the
// directories on the way to it are the session's own (other XDG bases, the
// user's linked entries) and stay in the home, and native/ holds history
// alone. Adopting the whole top-level dir would drag them into native/ and
// try to rename the link over native's store.
func TestKeepHomeHistory_LeavesALinkedNestedStoreAndItsSiblingsAlone(t *testing.T) {
	sd := t.TempDir()
	home := filepath.Join(sd, paths.SessionEngineHomesDirName, "oc")
	native := filepath.Join(sd, paths.NativeDirName, "oc")
	put(t, native, ".xdg/data/app/app.db", "history\n")
	put(t, home, ".xdg/config/app/config.json", "{}")
	put(t, home, ".xdg/data/app-cache/x", "session data\n")
	user := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(native, ".xdg/data/app"), filepath.Join(home, ".xdg/data/app")))
	require.NoError(t, os.Symlink(user, filepath.Join(home, ".xdg/data/fonts")))

	require.NoError(t, KeepHomeHistory(afero.NewOsFs(), sd))

	assert.Equal(t, "history\n", got(t, native, ".xdg/data/app/app.db"))
	assert.Equal(t, "{}", got(t, home, ".xdg/config/app/config.json"), "another base stays in the home")
	assert.Equal(t, "session data\n", got(t, home, ".xdg/data/app-cache/x"), "a sibling of the store stays in the home")
	assert.NoDirExists(t, filepath.Join(native, ".xdg/config"), "native/ holds history alone")
	assert.NoFileExists(t, filepath.Join(native, ".xdg/data/fonts"))
	target, err := os.Readlink(filepath.Join(home, ".xdg/data/fonts"))
	require.NoError(t, err)
	assert.Equal(t, user, target, "a user entry's link is left where it is")
}

// CHARACTERIZATION (a known limit, not the goal): a NESTED history store that
// is a real directory — a container run on a host whose directory links do
// not resolve in containers (Windows junctions), where the home starts from
// native/'s copy — has no link below it to tell the store's path from its
// siblings', so the top-level directory is adopted whole. Nothing is lost:
// the grown history and the new session reach native/. But the session's
// other content beside the store (another XDG base, a sibling app dir) moves
// into native/ too, where nothing reads it and nothing deletes it. Telling
// them apart needs the engine's TranscriptStoreRel at the caller (session
// reap, coordinator close), which neither carries today.
func TestKeepHomeHistory_ARealNestedStoreAdoptsItsTopLevelWhole(t *testing.T) {
	sd := t.TempDir()
	home := filepath.Join(sd, paths.SessionEngineHomesDirName, "oc")
	native := filepath.Join(sd, paths.NativeDirName, "oc")
	put(t, native, ".xdg/data/app/app.db", "history\n")
	put(t, home, ".xdg/data/app/app.db", "history\ngrown\n")
	put(t, home, ".xdg/data/app/storage/s2.json", "new session\n")
	put(t, home, ".xdg/config/app/config.json", "{}")
	put(t, home, ".xdg/data/app-cache/x", "session data\n")

	require.NoError(t, KeepHomeHistory(afero.NewOsFs(), sd))

	assert.Equal(t, "history\ngrown\n", got(t, native, ".xdg/data/app/app.db"), "the grown history is kept")
	assert.Equal(t, "new session\n", got(t, native, ".xdg/data/app/storage/s2.json"), "the run's new session is kept")
	assert.NoDirExists(t, filepath.Join(home, ".xdg"), "the top-level dir moved whole")
	assert.Equal(t, "{}", got(t, native, ".xdg/config/app/config.json"), "KNOWN LIMIT: another base rides along into native/")
	assert.Equal(t, "session data\n", got(t, native, ".xdg/data/app-cache/x"), "KNOWN LIMIT: a sibling of the store rides along into native/")
}
