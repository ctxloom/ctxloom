package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// deepPath is a path whose FLATTENED form alone exceeds NAME_MAX (255 bytes
// on every filesystem ctxloom runs on), before any timestamp or suffix is
// appended. This is the shape an agent worktree under a session's ephemeral
// directory produces, and the shape that made every record write fail.
func deepPath(leaf string) string {
	segs := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		segs = append(segs, "segment-"+strings.Repeat("x", 20))
	}
	return "/" + strings.Join(segs, "/") + "/" + leaf
}

func TestFlatName_BoundIsFixedRegardlessOfDepth(t *testing.T) {
	shallow := FlatName("/etc/hosts")
	deep := FlatName(deepPath("settings.json"))
	deeper := FlatName(deepPath(deepPath("settings.json")))

	require.Greater(t, len(deepPath("settings.json")), 255,
		"the fixture must be long enough to exceed NAME_MAX on its own, or the test proves nothing")
	assert.LessOrEqual(t, len(shallow), flatTailMax+len(flatSep)+flatHashLen)
	assert.Equal(t, flatTailMax+len(flatSep)+flatHashLen, len(deep),
		"a path past the tail bound must produce a name of exactly the bound")
	assert.Equal(t, len(deep), len(deeper),
		"doubling the depth must not change the name's length by one byte")
}

func TestFlatName_KeepsAReadableTail(t *testing.T) {
	name := FlatName(deepPath(".mcp.json"))

	assert.True(t, strings.Contains(name, "__.mcp.json"+flatSep),
		"the basename must survive in the readable tail so a records directory stays greppable: %s", name)
	assert.True(t, strings.HasPrefix(FlatName("/etc/hosts"), "__etc__hosts"+flatSep),
		"a short path keeps its whole flattened form as the tail")
}

func TestFlatName_DistinguishesPathsThatDifferOnlyBeyondTheTail(t *testing.T) {
	// Identical last 200+ bytes; the difference is at the ROOT, well past
	// where the readable tail stops. Truncate-only naming collides here and
	// one writer's record silently replaces the other's — the rejected shape.
	a := "/alpha" + deepPath("settings.json")
	b := "/bravo" + deepPath("settings.json")
	require.Equal(t, a[len(a)-200:], b[len(b)-200:], "fixture: the two paths must share their tail")

	assert.NotEqual(t, FlatName(a), FlatName(b),
		"two paths differing only before the tail must not flatten to one name")
}

func TestFlatName_HashesTheWholePath(t *testing.T) {
	p := deepPath("settings.json")
	sum := sha256.Sum256([]byte(filepath.ToSlash(p)))
	want := hex.EncodeToString(sum[:])[:flatHashLen]

	assert.True(t, strings.HasSuffix(FlatName(p), flatSep+want),
		"the name must end in a truncated sha256 of the FULL path, so the tail alone never decides identity")
}

func TestFlatName_IsDeterministic(t *testing.T) {
	p := deepPath("settings.json")
	assert.Equal(t, FlatName(p), FlatName(p))
}

// A deep protected path must produce a lock path whose final component fits
// NAME_MAX, with headroom for the lock suffix. This is the lock-side twin of
// the record bound: both name a file after a path they do not control.
func TestHomePathFor_BoundsADeepProtectedPath(t *testing.T) {
	home := testsupport.Isolate(t)
	protected := filepath.Join(home, deepPath("settings.json"))

	got, err := HomePathFor(protected)
	require.NoError(t, err)
	assert.Less(t, len(filepath.Base(got)), 255,
		"the lock's filename must fit NAME_MAX whatever the depth of the file it guards")
}

func TestProjectPathFor_BoundsADeepRelativePath(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, ".ctxloom", deepPath("state.yaml"))

	got, err := ProjectPathFor(protected)
	require.NoError(t, err)
	assert.Less(t, len(filepath.Base(got)), 255)
}
