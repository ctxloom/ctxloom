package confpatch

import (
	"os"
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

// TestRecordDirIsOwnerOnly: an undo record keeps the previous value of the key
// it undoes, so the directory holding records is owner-only. A directory that
// already exists looser — created by an older binary, or by hand — is tightened
// on the next write rather than trusted, because MkdirAll leaves an existing
// directory's mode alone.
func TestRecordDirIsOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "fresh directory", existing: false},
		{name: "pre-existing 0755 directory", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fs, dir := osStore(t)
			if tc.existing {
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.Chmod(dir, 0o755))
			}
			target := filepath.Join(t.TempDir(), ".mcp.json")
			testsupport.WriteFileString(t, fs, target, foreign, 0o644)

			res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
			require.NoError(t, err)

			di, err := os.Stat(dir)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), di.Mode().Perm(), "the records directory must be owner-only")
			fi, err := os.Stat(res.RecordPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "a record file must be owner-only")
		})
	}
}
