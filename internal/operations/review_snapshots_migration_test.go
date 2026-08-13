package operations

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// The approved-content snapshot store moved from cache/trust/objects to
// state/trust/objects: nothing rebuilds it, and cache/ promises the opposite —
// that its contents can be deleted and re-derived on demand. A user who took
// that promise at face value and wiped cache/ lost every diff base review had.
//
// The move needs a migration because the bytes are already on disk in projects
// that have been reviewing content for months, and a snapshot store that
// silently starts empty degrades every subsequent update review from a diff to
// a full-content dump — visible only as review being less useful than it was.

const (
	migrationHash    = "sha256:abc123"
	migrationContent = "the bytes a human approved"
)

// legacyObjectsPath is the pre-move location, spelled out here rather than
// taken from a helper: a test that derives the old path from the same code
// under test would keep passing if that code stopped looking there at all.
func legacyObjectsPath(baseDir string) string {
	return filepath.Join(baseDir, paths.CacheDir, paths.TrustFileName, paths.TrustObjectsDir)
}

func objectFile(dir, hash string) string {
	return filepath.Join(dir, snapshotFilename(hash))
}

// TestTrustSnapshots_FreshProjectWritesUnderState is the no-legacy case: a
// project that has never had a snapshot store gets one under state/, and the
// migration does not conjure a cache/ directory on the way past.
func TestTrustSnapshots_FreshProjectWritesUnderState(t *testing.T) {
	fs := afero.NewMemMapFs()

	writeTrustSnapshot(fs, ".ctxloom", migrationHash, []byte(migrationContent))

	got, ok := readTrustSnapshot(fs, ".ctxloom", migrationHash)
	require.True(t, ok)
	assert.Equal(t, migrationContent, got)

	exists, err := afero.Exists(fs, objectFile(paths.TrustObjectsPath(".ctxloom"), migrationHash))
	require.NoError(t, err)
	assert.True(t, exists, "a fresh store belongs under state/trust/objects")

	legacyExists, err := afero.DirExists(fs, legacyObjectsPath(".ctxloom"))
	require.NoError(t, err)
	assert.False(t, legacyExists, "nothing should re-create the retired cache location")
}

// TestTrustSnapshots_LegacyStoreMigratesEveryByte is the payload assertion: the
// migrated object must be byte-identical, not merely present. An empty-source
// guard sits in front of the comparison because this project's characteristic
// failure is a writer that reports success and moves zero bytes — a comparison
// of "" against "" would pass exactly then.
func TestTrustSnapshots_LegacyStoreMigratesEveryByte(t *testing.T) {
	fs := afero.NewMemMapFs()
	legacy := legacyObjectsPath(".ctxloom")
	require.NoError(t, fs.MkdirAll(legacy, 0o755))
	require.NoError(t, afero.WriteFile(fs, objectFile(legacy, migrationHash), []byte(migrationContent), 0o644))
	second := "sha256:def456"
	require.NoError(t, afero.WriteFile(fs, objectFile(legacy, second), []byte("a second approved body"), 0o644))

	seeded, err := afero.ReadFile(fs, objectFile(legacy, migrationHash))
	require.NoError(t, err)
	require.NotEmpty(t, seeded, "the fixture must actually have bytes, or the comparison below is vacuous")

	got, ok := readTrustSnapshot(fs, ".ctxloom", migrationHash)
	require.True(t, ok, "a legacy snapshot must still be readable after the move")
	assert.Equal(t, migrationContent, got)

	migrated, err := afero.ReadFile(fs, objectFile(paths.TrustObjectsPath(".ctxloom"), migrationHash))
	require.NoError(t, err, "the object must exist at the new location")
	assert.Equal(t, seeded, migrated, "the migrated object must be byte-identical")

	otherMigrated, err := afero.ReadFile(fs, objectFile(paths.TrustObjectsPath(".ctxloom"), second))
	require.NoError(t, err, "every object moves, not just the one that was read")
	assert.Equal(t, []byte("a second approved body"), otherMigrated)

	legacyExists, err := afero.DirExists(fs, legacy)
	require.NoError(t, err)
	assert.False(t, legacyExists, "the legacy store must be gone, or a later run migrates it again over the current one")
}

// renameFailsFs is a filesystem on which a CROSS-DIRECTORY rename always
// fails, which is what a cross-device move looks like: EXDEV is returned no
// matter how many times it is retried, and cache/ and state/ landing on
// different mounts takes one symlinked .ctxloom/cache to arrange.
//
// A same-directory rename always SUCCEEDS here, deliberately: it is what a
// real EXDEV never touches (the two names share a parent, hence a device, by
// construction), and it is exactly the shape copyTrustObjects's per-file
// writes now use — each goes through iox.WriteFileAtomicFs, whose commit step
// is a rename of a unique temp file into place WITHIN THE SAME destination
// directory (see iox's own doc: "a UNIQUE temp file in the destination
// directory... renamed over path"). Failing every rename unconditionally, as
// this fixture used to, stopped simulating "the top-level move crosses
// devices" and started also breaking the fallback COPY's own writes, which
// results in the test asserting cross-device recovery paths never exercise:
// on any real filesystem the same-directory case never fails this way.
type renameFailsFs struct {
	afero.Fs
}

func (f renameFailsFs) Rename(oldname, newname string) error {
	if filepath.Dir(oldname) != filepath.Dir(newname) {
		return errors.New("invalid cross-device link")
	}
	return f.Fs.Rename(oldname, newname)
}

// TestTrustSnapshots_CrossDeviceMoveCopiesThenRemoves covers the fallback the
// happy path never reaches. It is asserted on BYTES at the destination and on
// the source being gone afterwards: a fallback that creates the destination
// directory and copies nothing would satisfy "the file is where I expect" while
// having thrown away every diff base, which is this project's characteristic
// failure written into a migration.
func TestTrustSnapshots_CrossDeviceMoveCopiesThenRemoves(t *testing.T) {
	fs := renameFailsFs{afero.NewMemMapFs()}
	legacy := legacyObjectsPath(".ctxloom")
	require.NoError(t, fs.MkdirAll(legacy, 0o755))
	require.NoError(t, afero.WriteFile(fs, objectFile(legacy, migrationHash), []byte(migrationContent), 0o644))

	got, ok := readTrustSnapshot(fs, ".ctxloom", migrationHash)
	require.True(t, ok, "a cross-device move must still deliver the snapshot")
	assert.Equal(t, migrationContent, got)

	migrated, err := afero.ReadFile(fs, objectFile(paths.TrustObjectsPath(".ctxloom"), migrationHash))
	require.NoError(t, err)
	require.NotEmpty(t, migrated, "a copy that lands zero bytes is the failure this assertion exists for")
	assert.Equal(t, []byte(migrationContent), migrated)

	legacyExists, err := afero.DirExists(fs, legacy)
	require.NoError(t, err)
	assert.False(t, legacyExists, "the source is removed only after every byte has landed")
}

// TestTrustSnapshots_BothStoresExistMovesNothing pins the refusal. Merging two
// content-addressed stores looks safe — same hash, same bytes, by
// construction — right up to the moment one of them was written by a build
// with a different hashing rule, at which point overwriting silently replaces
// the diff base a human's approval actually covered. The current store is the
// live one, it is left exactly as it is, and the user is told both paths.
func TestTrustSnapshots_BothStoresExistMovesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	legacy := legacyObjectsPath(".ctxloom")
	current := paths.TrustObjectsPath(".ctxloom")
	require.NoError(t, fs.MkdirAll(legacy, 0o755))
	require.NoError(t, afero.WriteFile(fs, objectFile(legacy, migrationHash), []byte("the LEGACY body"), 0o644))
	require.NoError(t, fs.MkdirAll(current, 0o755))
	require.NoError(t, afero.WriteFile(fs, objectFile(current, migrationHash), []byte("the CURRENT body"), 0o644))

	got, ok := readTrustSnapshot(fs, ".ctxloom", migrationHash)
	require.True(t, ok)
	assert.Equal(t, "the CURRENT body", got, "the store under state/ is the live one")

	stillLegacy, err := afero.ReadFile(fs, objectFile(legacy, migrationHash))
	require.NoError(t, err, "nothing may be deleted when the two cannot be reconciled")
	assert.Equal(t, []byte("the LEGACY body"), stillLegacy)
}

// TestTrustSnapshots_SymlinkedLegacyStoreIsNeverFollowed uses a REAL filesystem
// because that is the only one that has symlinks to refuse. A legacy path that
// is a link (a developer pointing cache/trust at a shared directory, or at
// another checkout) must not be moved: a rename would relocate whatever it
// points at, out from under whoever else is using it.
func TestTrustSnapshots_SymlinkedLegacyStoreIsNeverFollowed(t *testing.T) {
	root := t.TempDir()
	fs := afero.NewOsFs()
	baseDir := filepath.Join(root, paths.AppDirName)

	real := filepath.Join(root, "elsewhere")
	require.NoError(t, os.MkdirAll(real, 0o755))
	require.NoError(t, os.WriteFile(objectFile(real, migrationHash), []byte(migrationContent), 0o644))

	legacy := legacyObjectsPath(baseDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	require.NoError(t, os.Symlink(real, legacy))

	_, _ = readTrustSnapshot(fs, baseDir, migrationHash)

	info, err := os.Lstat(legacy)
	require.NoError(t, err, "the link itself must survive")
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the legacy path must still be a symlink, not a moved directory")

	body, err := os.ReadFile(objectFile(real, migrationHash))
	require.NoError(t, err, "the directory the link points at must be untouched")
	assert.Equal(t, migrationContent, string(body))
}
