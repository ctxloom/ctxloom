package remote

import (
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

const schemaverLockKey = ident.BundleKey("ctxloom+git://example.test/repo//bundles/kit")

func lockBody(versionKey string, version int) string {
	return "# kept by hand\n" + versionKey + ": " + strconv.Itoa(version) + "\n" +
		"bundles:\n  " + string(schemaverLockKey) + ":\n    sha: abc123\n"
}

// The const the rest of the code stamps and the kind's derived Current are
// one number: a step added without bumping LockfileVersion (or the reverse)
// fails here.
func TestLockfileKind_CurrentIsLockfileVersion(t *testing.T) {
	assert.Equal(t, LockfileVersion, lockfileKind.Current())
}

func TestLoad_CurrentSchemaVersionLoads(t *testing.T) {
	lm := lockWithBody(t, lockBody(schemaver.Key, LockfileVersion))
	lock, err := lm.Load()
	require.NoError(t, err)
	_, ok := lock.GetEntry(ItemTypeBundle, schemaverLockKey)
	assert.True(t, ok)
	assert.Equal(t, LockfileVersion, lock.Version)
}

// `version` is not a spelling of schemaver.Key: a lockfile that declares its
// generation only that way declares none, and is refused.
func TestLoad_VersionKeyIsNotTheGeneration(t *testing.T) {
	_, err := lockWithBody(t, lockBody("version", LockfileVersion)).Load()
	require.Error(t, err)
}

func TestLoad_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	lm := lockWithBody(t, lockBody(schemaver.Key, LockfileVersion+1))
	_, err := lm.Load()
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, LockfileVersion+1, ve.Found)
	assert.Equal(t, LockfileVersion, ve.Current)
}

// A newer lockfile is not this binary's to overwrite either: the write would
// downgrade it, dropping whatever the newer format records.
func TestSave_RefusesOverwritingANewerLockfile(t *testing.T) {
	body := lockBody(schemaver.Key, LockfileVersion+1)
	lm := lockWithBody(t, body)
	err := lm.Save(&Lockfile{Bundles: map[ident.BundleKey]LockEntry{schemaverLockKey: {SHA: "def456"}}})
	require.ErrorIs(t, err, schemaver.ErrNewer)
	onDisk, rerr := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, rerr)
	assert.Equal(t, body, string(onDisk))
}

// The retired per-entry ctxloom_version key is dropped in memory and is gone
// from disk only once something writes the lockfile.
func TestLoad_RetiredEntryFieldIsDroppedOnTheNextSaveNotOnRead(t *testing.T) {
	body := "schema_version: 2\nbundles:\n  " + string(schemaverLockKey) + ":\n    sha: abc123\n    ctxloom_version: v1\n"
	lm := lockWithBody(t, body)
	lock, err := lm.Load()
	require.NoError(t, err)
	onDisk, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	assert.Equal(t, body, string(onDisk), "a read must not write")

	require.NoError(t, lm.Save(lock))
	reloaded, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	assert.NotContains(t, string(reloaded), "ctxloom_version")
	assert.Contains(t, string(reloaded), schemaver.Key+": "+strconv.Itoa(LockfileVersion))
}

// A lockfile with no version key at all predates key-by-identity: refused as
// a retired key form, exactly like an explicit older version.
func TestLoad_KeylessIsARetiredKeyForm(t *testing.T) {
	lm := lockWithBody(t, "bundles:\n  "+string(schemaverLockKey)+":\n    sha: abc123\n    held: true\n")
	_, err := lm.Load()
	require.ErrorIs(t, err, ErrLockKeyFormRetired)
	assert.Contains(t, err.Error(), string(schemaverLockKey), "the refusal lists the held entry")
}

// A lockfile that is not YAML is reported as the parse failure it is — the
// user fixes or deletes the file — not as an unreadable format version, which
// would point them at the wrong fault.
func TestLoad_MalformedIsAParseFailureNotAVersionFault(t *testing.T) {
	lm := lockWithBody(t, "bundles: [unterminated\n")
	_, err := lm.Load()
	require.Error(t, err)
	var ve *schemaver.VersionError
	assert.NotErrorAs(t, err, &ve)
}
