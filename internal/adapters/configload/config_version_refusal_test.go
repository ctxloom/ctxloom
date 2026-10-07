package configload

import (
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const refusalAppDir = "/project/" + paths.AppDirName

// seedProjectConfig writes body as the project config on a fresh in-memory fs.
func seedProjectConfig(t *testing.T, body string) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(refusalAppDir, 0o755))
	testsupport.WriteFile(t, fs, paths.ConfigPath(refusalAppDir), []byte(body), 0o644)
	return fs
}

// loadFindings loads fs's project config and returns the findings the load
// raised, with the strictness gate isolated to this test.
func loadFindings(t *testing.T, fs afero.Fs, opts ...Option) []report.Finding {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	mark := strictness.Checkpoint()
	_, err := Load(append([]Option{WithRoot(safefs.NewMem(fs)), WithAppDir(refusalAppDir)}, opts...)...)
	require.NoError(t, err, "a version refusal is a FINDING, not a load error: the gate decides")
	return strictness.Since(mark)
}

// loadRefusalFindings loads body as the project config and returns the findings
// the load raised.
func loadRefusalFindings(t *testing.T, body string) []report.Finding {
	t.Helper()
	return loadFindings(t, seedProjectConfig(t, body))
}

// writeUpgradesOn turns the process-wide --write-upgrades switch on for the
// test's duration.
func writeUpgradesOn(t *testing.T) {
	t.Helper()
	flags := pflag.NewFlagSet(t.Name(), pflag.ContinueOnError)
	schemaver.BindWriteUpgrades(flags)
	require.NoError(t, flags.Set(schemaver.WriteUpgradesFlag, "true"))
	// Binding resets the switch, which is the only way to turn it back off.
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })
}

func versioned(n int, rest string) string {
	return fmt.Sprintf("%s: %d\n%s", schemaver.Key, n, rest)
}

const refusalBody = "llm:\n  defaults:\n    primary: claude-code\n"

// The kind's current generation IS the config version every writer stamps.
func TestConfigKind_CurrentIsTheConfigVersion(t *testing.T) {
	assert.Equal(t, config.CurrentConfigVersion, configKind.Current())
}

// A config older than the oldest generation this binary migrates is refused
// rather than read as though its retired key names still meant something. The
// finding names the file, the version it declares and the version required.
func TestLoad_OlderThanOldest_IsRefusedWithAnActionableFinding(t *testing.T) {
	older := configKind.Oldest() - 1
	found := loadRefusalFindings(t, versioned(older, refusalBody))

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, report.KindMigration, found[0].Kind)
	assert.Contains(t, found[0].Text, fmt.Sprintf("%s %d", schemaver.Key, older), "the finding must quote the version the file declares")
	assert.Contains(t, found[0].Text, fmt.Sprint(configKind.Oldest()), "and the oldest version this build reads")
	assert.Contains(t, found[0].Text, paths.ConfigPath(refusalAppDir), "and name the file")
	assert.Contains(t, found[0].Remedy, "ctxloom init", "the remedy is re-scaffolding, and the finding must say so")
}

// A document with no version key is generation 0 — older than every numbered
// schema, not exempt from the check. Treating absent as current is the
// silent-acceptance direction.
func TestLoad_UnversionedConfig_IsRefused(t *testing.T) {
	found := loadRefusalFindings(t, refusalBody)

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, report.KindMigration, found[0].Kind)
	assert.Contains(t, found[0].Text, fmt.Sprintf("%s %d", schemaver.Key, 0))
}

// A config written by a NEWER ctxloom is refused, naming both numbers: an
// older binary cannot know what the newer format's keys mean, and reading it
// anyway silently drops or misreads them.
func TestLoad_NewerThanCurrent_IsRefusedNamingBothNumbers(t *testing.T) {
	newer := configKind.Current() + 1
	found := loadRefusalFindings(t, versioned(newer, refusalBody))

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, report.KindMigration, found[0].Kind)
	assert.Contains(t, found[0].Text, fmt.Sprintf("%s %d", schemaver.Key, newer), "the version the file declares")
	assert.Contains(t, found[0].Text, fmt.Sprint(configKind.Current()), "and the version this binary reads")
	assert.Contains(t, found[0].Text, paths.ConfigPath(refusalAppDir), "and the file")
}

// The current version is accepted silently. Without this, a refusal that fired
// on EVERY config would pass the tests above while breaking every user.
func TestLoad_CurrentVersion_RaisesNoFinding(t *testing.T) {
	found := loadRefusalFindings(t, versioned(config.CurrentConfigVersion, refusalBody))

	assert.Empty(t, found, "a current config must raise nothing: %+v", found)
}

// `version` is not a spelling of schema_version: a config that declares its
// generation only that way declares none, and is refused as generation 0.
func TestLoad_VersionKey_IsNotTheGeneration(t *testing.T) {
	found := loadRefusalFindings(t, fmt.Sprintf("version: %d\n%s", config.CurrentConfigVersion, refusalBody))

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, report.KindMigration, found[0].Kind)
	assert.Contains(t, found[0].Text, fmt.Sprintf("%s %d", schemaver.Key, 0))
}

// A file already current is never rewritten, flag or not.
func TestLoad_CurrentVersion_WithWriteUpgrades_WritesNothing(t *testing.T) {
	writeUpgradesOn(t)
	fs := seedProjectConfig(t, versioned(config.CurrentConfigVersion, refusalBody))

	assert.Empty(t, loadFindings(t, fs))

	_, err := fs.Stat(paths.ConfigPath(refusalAppDir) + schemaver.BackupSuffix)
	assert.ErrorIs(t, err, afero.ErrFileNotFound, "nothing to persist, so no write and no backup")
}

// The profile-ref canonicalization runs after the version gate, in memory,
// and --write-upgrades persists it along with the migration.
func TestLoad_CanonicalizedProfileRefs_PersistOnlyWithWriteUpgrades(t *testing.T) {
	const short, canonical = "short-ref", "canonical-ref"
	body := versioned(config.CurrentConfigVersion, "agents:\n  dev:\n    llm: claude-code\n    profiles:\n      - "+short+"\n")
	canon := WithProfileRefCanonicalizer(func(_ *config.Config, ref string) string {
		if ref == short {
			return canonical
		}
		return ref
	})
	path := paths.ConfigPath(refusalAppDir)

	t.Run("without the flag the file keeps the short ref", func(t *testing.T) {
		fs := seedProjectConfig(t, body)
		loadFindings(t, fs, canon)
		got, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
	})
	t.Run("with the flag the canonical ref is written", func(t *testing.T) {
		writeUpgradesOn(t)
		fs := seedProjectConfig(t, body)
		loadFindings(t, fs, canon)
		got, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Contains(t, string(got), canonical)
		assert.NotContains(t, string(got), short)
	})
}
