package remote

import (
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
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

// withWriteUpgrades turns the process-wide --write-upgrades switch on for one
// test and off again after it.
func withWriteUpgrades(t *testing.T) {
	t.Helper()
	fs := pflag.NewFlagSet(t.Name(), pflag.ContinueOnError)
	schemaver.BindWriteUpgrades(fs)
	require.NoError(t, fs.Parse([]string{"--" + schemaver.WriteUpgradesFlag}))
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })
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

// A lockfile still spelling its version `version` loads, and the read leaves
// the file byte-for-byte as it was: the rename is in memory only.
func TestLoad_LegacyKeyLoadsAndReadingNeverWrites(t *testing.T) {
	body := lockBody("version", LockfileVersion)
	lm := lockWithBody(t, body)
	lock, err := lm.Load()
	require.NoError(t, err)
	_, ok := lock.GetEntry(ItemTypeBundle, schemaverLockKey)
	assert.True(t, ok)
	assert.Equal(t, LockfileVersion, lock.Version)

	onDisk, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	assert.Equal(t, body, string(onDisk), "a read must not write")
	_, err = lm.FS().Stat(lm.Path() + schemaver.BackupSuffix)
	assert.Error(t, err, "and must leave no backup")
}

func TestLoad_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	for _, key := range []string{schemaver.Key, "version"} {
		t.Run(key, func(t *testing.T) {
			lm := lockWithBody(t, lockBody(key, LockfileVersion+1))
			_, err := lm.Load()
			require.ErrorIs(t, err, schemaver.ErrNewer)
			var ve *schemaver.VersionError
			require.ErrorAs(t, err, &ve)
			assert.Equal(t, LockfileVersion+1, ve.Found)
			assert.Equal(t, LockfileVersion, ve.Current)
		})
	}
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
	body := "version: 2\nbundles:\n  " + string(schemaverLockKey) + ":\n    sha: abc123\n    ctxloom_version: v1\n"
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

// --write-upgrades persists the in-memory migration and leaves no backup: the
// lockfile is committed, so git holds the old bytes.
func TestLoad_WriteUpgradesPersistsTheRename(t *testing.T) {
	withWriteUpgrades(t)
	body := lockBody("version", LockfileVersion)
	lm := lockWithBody(t, body)
	_, err := lm.Load()
	require.NoError(t, err)

	onDisk, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), schemaver.Key+": "+strconv.Itoa(LockfileVersion))
	assert.Contains(t, string(onDisk), "# kept by hand", "the write-back keeps comments")
	backup, err := afero.Exists(lm.FS(), lm.Path()+schemaver.BackupSuffix)
	require.NoError(t, err)
	assert.False(t, backup, "no .bak is left beside a committed lockfile")
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
