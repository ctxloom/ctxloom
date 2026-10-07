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

// A lockfile with no version key at all declares no generation: refused.
func TestLoad_KeylessIsRefused(t *testing.T) {
	lm := lockWithBody(t, "bundles:\n  "+string(schemaverLockKey)+":\n    sha: abc123\n    held: true\n")
	_, err := lm.Load()
	require.ErrorIs(t, err, schemaver.ErrTooOld)
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

// v2LockWithTimes is a generation-2 lock as every release before 3 wrote it:
// a write time on the file and a fetch time on each entry.
const v2LockWithTimes = "# kept by hand\nschema_version: 2\nlocked_at: 2026-10-01T12:00:00Z\nbundles:\n  " +
	string(schemaverLockKey) + ":\n    sha: abc123\n    fetched_at: 2026-09-30T08:00:00Z\n    held: true\n"

// A generation-2 lock still carries the two times generation 3 dropped. Strict
// decoding refuses any field the format does not model, so the migration must
// remove them first: the lock loads with its pins and holds intact, and
// reading it writes nothing.
func TestLoad_V2WithTimesMigratesToCurrent(t *testing.T) {
	lm := lockWithBody(t, v2LockWithTimes)
	lock, err := lm.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(ItemTypeBundle, schemaverLockKey)
	require.True(t, ok)
	assert.Equal(t, LockEntry{SHA: "abc123", Held: true}, entry)
	onDisk, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	assert.Equal(t, v2LockWithTimes, string(onDisk), "a load never writes without --write-upgrades")
}

// Under --write-upgrades the migrated generation-2 lock reaches disk at the
// current generation, without either time and with its comment kept.
func TestLoad_V2WriteUpgradesPersistsWithoutTimes(t *testing.T) {
	flags := pflag.NewFlagSet("t", pflag.ContinueOnError)
	schemaver.BindWriteUpgrades(flags)
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })
	require.NoError(t, flags.Parse([]string{"--" + schemaver.WriteUpgradesFlag}))

	lm := lockWithBody(t, v2LockWithTimes)
	_, err := lm.Load()
	require.NoError(t, err)
	onDisk, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	assert.Equal(t, "# kept by hand\nschema_version: "+strconv.Itoa(LockfileVersion)+"\nbundles:\n  "+
		string(schemaverLockKey)+":\n    sha: abc123\n    held: true\n", string(onDisk))
}
