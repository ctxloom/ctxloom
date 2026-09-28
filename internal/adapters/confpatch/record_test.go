package confpatch

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// deepTarget is a target whose FLATTENED form alone exceeds NAME_MAX. It is
// the shape an agent worktree under a session's ephemeral directory produces,
// and it is built under root so a REAL filesystem can be asked to hold it.
func deepTarget(root, leaf string) string {
	segs := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		segs = append(segs, "segment-"+strings.Repeat("x", 20))
	}
	return filepath.Join(append([]string{root}, append(segs, leaf)...)...)
}

// osStore is a Store on the REAL filesystem. MemMapFs enforces no NAME_MAX,
// so a filename bound can only be proven against a filesystem that has one.
func osStore(t *testing.T) (*Store, afero.Fs, string) {
	t.Helper()
	fs := afero.NewOsFs()
	dir := filepath.Join(t.TempDir(), "records")
	s, err := NewStore(fs, dir, "ctxloom")
	require.NoError(t, err)
	return s, fs, dir
}

func TestApplyWritesAndReadsBackARecordForATargetPastNameMax(t *testing.T) {
	s, fs, dir := osStore(t)
	target := deepTarget(t.TempDir(), ".mcp.json")
	require.Greater(t, len(strings.ReplaceAll(target, "/", "__")), 255,
		"fixture: the flattened target must exceed NAME_MAX or the test proves nothing")
	require.NoError(t, fs.MkdirAll(filepath.Dir(target), 0o755))
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
	require.NoError(t, err, "a deep target must write a record, not fail on the filesystem's name bound")
	assert.Less(t, len(filepath.Base(res.RecordPath)), 255)
	assert.Equal(t, dir, filepath.Dir(res.RecordPath))

	rec, found, err := s.Last(target)
	require.NoError(t, err)
	require.True(t, found, "the record just written must be found by the same target")
	assert.Equal(t, target, rec.Targets[0].Target)
}

func TestTwoDeepTargetsDifferingOnlyBeforeTheTailDoNotCollide(t *testing.T) {
	s, fs, _ := osStore(t)
	root := t.TempDir()
	a := deepTarget(filepath.Join(root, "alpha"), ".mcp.json")
	b := deepTarget(filepath.Join(root, "bravo"), ".mcp.json")
	require.Equal(t, a[len(a)-200:], b[len(b)-200:], "fixture: the two targets must share their tail")
	for _, target := range []string{a, b} {
		require.NoError(t, fs.MkdirAll(filepath.Dir(target), 0o755))
		testsupport.WriteFileString(t, fs, target, foreign, 0o644)
	}

	resA, err := s.Apply(fs, a, setServer("ctxloom", map[string]any{"command": "/opt/a/ctxloom"}))
	require.NoError(t, err)
	resB, err := s.Apply(fs, b, setServer("ctxloom", map[string]any{"command": "/opt/b/ctxloom"}))
	require.NoError(t, err)
	assert.NotEqual(t, resA.RecordPath, resB.RecordPath)

	// Effect, not name: each target's live record is ITS record. A scheme
	// that truncated the flattened path would hand b's record to a — and the
	// second apply would prune the first's record as "superseded".
	recA, found, err := s.Last(a)
	require.NoError(t, err)
	require.True(t, found, "a's record must survive b's apply")
	assert.Equal(t, a, recA.Targets[0].Target)
	recB, found, err := s.Last(b)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, b, recB.Targets[0].Target)
}

// A records directory already prepared on the real filesystem is written
// through fsstatic's copy-on-write overlay without the overlay being asked to
// change it: its Chmod of a directory in the base fails, and on Windows, where
// a directory never reports mode 0700, a "chmod unless already 0700" guard
// asked it every time.
func TestEnsureRecordDir_ThroughAnOverlayOverAPreparedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	require.NoError(t, EnsureRecordDir(afero.NewOsFs(), dir))

	overlay := afero.NewCopyOnWriteFs(afero.NewOsFs(), afero.NewMemMapFs())
	require.NoError(t, EnsureRecordDir(overlay, dir))
}
