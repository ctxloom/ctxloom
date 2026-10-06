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
