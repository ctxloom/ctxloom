package coord

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// sanitizeKey mapped the separators and "..", but never a BARE "."
// (nor any key that reduces to one), so a project key of "." resolved
// stateDirForProject to ~/.ctxloom/coord ITSELF — the root every project's own
// state dir lives under, and the root discover.List globs. Every project
// landing on that key would have shared one owner.pid/runs.jsonl/mailbox.jsonl
// set, and the journals would have sat among the per-project directories rather
// than inside one.
//
// The assertion is on the resolved PATH, not on the returned string, because
// the defect is only visible after filepath.Join collapses the "." — the key
// itself looks harmless.
func TestStateDirForProject_DotOnlyKeyNeverResolvesToTheCoordRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads $HOME on unix; confirm the override took before
	// asserting on paths derived from it.
	got, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, home, got, "the HOME override must be what os.UserHomeDir resolves")

	root := filepath.Join(home, paths.AppDirName, coordDirName)

	for _, key := range []string{".", "..", "...", "./.", "/."} {
		dir, err := stateDirForProject(key)
		require.NoError(t, err, "key %q", key)
		assert.NotEqual(t, root, filepath.Clean(dir),
			"key %q must not resolve the state dir to the coord root itself", key)
		assert.Equal(t, root, filepath.Dir(filepath.Clean(dir)),
			"key %q must resolve to a single segment DIRECTLY under the coord root", key)
	}
}

// A dot-only key resolves to the same "no usable identity" bucket an empty key
// does; a key that merely CONTAINS dots keeps its own distinct segment, so
// hardening the degenerate case must not collapse ordinary keys together.
func TestSanitizeKey_DotHandling(t *testing.T) {
	// The literal (not the constant) is deliberate: this value NAMES A
	// DIRECTORY holding live journals, so the golden must notice a rename.
	assert.Equal(t, "default", sanitizeKey(""))
	assert.Equal(t, "default", sanitizeKey("."),
		"a bare dot is the one dot form the separator replacer cannot reach")
	assert.Equal(t, "-", sanitizeKey(".."),
		"traversal is mapped by the replacer, which already yields a usable segment")
	assert.Equal(t, ".-.", sanitizeKey("./."))

	assert.Equal(t, "proj.one", sanitizeKey("proj.one"))
	assert.Equal(t, ".hidden", sanitizeKey(".hidden"))
	assert.Equal(t, "a-b", sanitizeKey("a/b"))
	assert.Equal(t, "a-b", sanitizeKey("a..b"), "traversal is still mapped, not preserved")
	assert.NotEqual(t, sanitizeKey("proj.one"), sanitizeKey("proj.two"))
}

// TestStateDirForProject_ResolvesUnderHomeDotCtxloomCoord pins
// stateDirForProject's resolved path against a HARD-CODED ".ctxloom"/"coord"
// segment pair, independent of paths.CoordDirName/paths.AppDirName — unlike
// TestStateDirForProject_DotOnlyKeyNeverResolvesToTheCoordRoot above (which
// builds its "root" from the same coordDirName identifier production uses,
// so it cannot catch a change to that identifier's VALUE), this test fails
// if either segment's spelling ever drifts.
func TestStateDirForProject_ResolvesUnderHomeDotCtxloomCoord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, home, got, "the HOME override must be what os.UserHomeDir resolves")

	dir, err := stateDirForProject("proj-key")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ctxloom", "coord", "proj-key"), dir)
}
