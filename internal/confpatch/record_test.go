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

// legacyRecordName is the filename scheme records on disk were written under
// before the bound: the whole flattened target, then the timestamp and
// suffix. Spelled out here rather than derived, because this IS the shape the
// migration must recognise.
func legacyRecordName(target, stamp string) string {
	return strings.ReplaceAll(filepath.ToSlash(target), "/", "__") + "__" + stamp + recordFileSuffix
}

// plantLegacyRecord writes a valid record for target under the legacy name in
// dir, returning that name. It obtains a real record body by applying once and
// moving the result, so the body is whatever this package writes today.
func plantLegacyRecord(t *testing.T, s *Store, fs afero.Fs, dir, target, stamp string) string {
	t.Helper()
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)
	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
	require.NoError(t, err)
	body, err := afero.ReadFile(fs, res.RecordPath)
	require.NoError(t, err)
	require.NoError(t, fs.Remove(res.RecordPath))

	old := legacyRecordName(target, stamp)
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, old), body, 0o644))
	return old
}

func listRecords(t *testing.T, fs afero.Fs, dir string) []string {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestLastFindsAnOldStyleRecordAfterRenamingIt(t *testing.T) {
	s, fs := newStore(t)
	const dir = "/home/u/.ctxloom/records"
	const target = "/proj/mcp.json"
	old := plantLegacyRecord(t, s, fs, dir, target, "20260901T000000.000000000Z")

	fresh, err := NewStore(fs, dir, "ctxloom")
	require.NoError(t, err)
	rec, found, err := fresh.Last(target)
	require.NoError(t, err)
	require.True(t, found, "a record written under the old name must still be found")
	assert.Equal(t, target, rec.Targets[0].Target)

	names := listRecords(t, fs, dir)
	require.Len(t, names, 1, "the rename must neither lose nor duplicate the record")
	assert.NotEqual(t, old, names[0], "the record must now carry the new name")
	assert.True(t, strings.HasSuffix(names[0], "__20260901T000000.000000000Z"+recordFileSuffix),
		"the timestamp part of the name is preserved across the rename: %s", names[0])
}

func TestTheRenameIsIdempotent(t *testing.T) {
	s, fs := newStore(t)
	const dir = "/home/u/.ctxloom/records"
	const target = "/proj/mcp.json"
	plantLegacyRecord(t, s, fs, dir, target, "20260901T000000.000000000Z")

	first, err := NewStore(fs, dir, "ctxloom")
	require.NoError(t, err)
	_, found, err := first.Last(target)
	require.NoError(t, err)
	require.True(t, found)
	after1 := listRecords(t, fs, dir)
	body1, err := afero.ReadFile(fs, filepath.Join(dir, after1[0]))
	require.NoError(t, err)

	// A second launch, a second first-read.
	second, err := NewStore(fs, dir, "ctxloom")
	require.NoError(t, err)
	_, found, err = second.Last(target)
	require.NoError(t, err)
	require.True(t, found)
	after2 := listRecords(t, fs, dir)
	body2, err := afero.ReadFile(fs, filepath.Join(dir, after2[0]))
	require.NoError(t, err)

	assert.Equal(t, after1, after2, "a second pass must change nothing")
	assert.Equal(t, body1, body2, "a rename must not touch the record's bytes")
}

func TestTheRenameReachesANestedStoreDirectory(t *testing.T) {
	// taskloom keeps its own store in a SUBDIRECTORY of ctxloom's records dir.
	// The top-level store's first read renames records there too, so a store
	// nobody re-opens does not keep unreachable records forever.
	fs := afero.NewMemMapFs()
	const top = "/home/u/.ctxloom/records"
	nestedDir := filepath.Join(top, "taskloom")
	nested, err := NewStore(fs, nestedDir, "taskloom")
	require.NoError(t, err)
	const target = "/proj/.mcp.json"
	old := plantLegacyRecord(t, nested, fs, nestedDir, target, "20260901T000000.000000000Z")

	topStore, err := NewStore(fs, top, "ctxloom")
	require.NoError(t, err)
	_, _, err = topStore.Last("/unrelated/file.json")
	require.NoError(t, err)

	names := listRecords(t, fs, nestedDir)
	require.Len(t, names, 1)
	assert.NotEqual(t, old, names[0], "the nested store's record must have been renamed by the top-level read")

	rec, found, err := nested.Last(target)
	require.NoError(t, err)
	require.True(t, found, "the nested store must still find its record by target")
	assert.Equal(t, target, rec.Targets[0].Target)
}

func TestTheRenameLeavesAnUnrecognisedFileAlone(t *testing.T) {
	s, fs := newStore(t)
	const dir = "/home/u/.ctxloom/records"
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	stray := filepath.Join(dir, "notes"+recordFileSuffix)
	require.NoError(t, afero.WriteFile(fs, stray, []byte("not: a record\n"), 0o644))

	_, found, err := s.Last("/proj/mcp.json")
	require.NoError(t, err)
	assert.False(t, found)
	exists, err := afero.Exists(fs, stray)
	require.NoError(t, err)
	assert.True(t, exists, "a file whose body names no target is not the migration's to touch")
}
