package configload

import (
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
	_, err := Load(append([]Option{WithFS(fs), WithAppDir(refusalAppDir)}, opts...)...)
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

// The kind's current generation IS the config version every writer stamps:
// the kind derives it from its steps and config declares it, so the two part
// whenever a step is added without the bump, or the bump without a step.
func TestConfigKind_CurrentIsTheConfigVersion(t *testing.T) {
	assert.Equal(t, config.CurrentConfigVersion, configKind.Current())
}

// A config older than the oldest generation this binary migrates is refused
// rather than read as though its retired key names still meant something. The
// finding names the file, the version it declares and the version required.
func TestLoad_OlderThanOldest_IsRefusedWithAnActionableFinding(t *testing.T) {
	older := configKind.Oldest - 1
	found := loadRefusalFindings(t, versioned(older, refusalBody))

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, report.KindMigration, found[0].Kind)
	assert.Contains(t, found[0].Text, fmt.Sprintf("%s %d", schemaver.Key, older), "the finding must quote the version the file declares")
	assert.Contains(t, found[0].Text, fmt.Sprint(configKind.Oldest), "and the oldest version this build reads")
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

// The legacy spelling `version: N` is the same generation under its old key:
// it loads, and its values are read.
func TestLoad_LegacyVersionKey_Loads(t *testing.T) {
	fs := seedProjectConfig(t, fmt.Sprintf("%s: %d\n%s", configKind.LegacyKey, config.CurrentConfigVersion, refusalBody))
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	mark := strictness.Checkpoint()

	cfg, err := Load(WithFS(fs), WithAppDir(refusalAppDir))
	require.NoError(t, err)
	assert.Empty(t, strictness.Since(mark), "a legacy-keyed current config must raise nothing")
	assert.Equal(t, "claude-code", cfg.GetLMConfig().Defaults.Primary, "the legacy-keyed file's values must be read")
}

// Without --write-upgrades an in-memory migration never touches the file.
func TestLoad_LegacyVersionKey_WithoutWriteUpgrades_LeavesTheFileAlone(t *testing.T) {
	body := fmt.Sprintf("%s: %d\n%s", configKind.LegacyKey, config.CurrentConfigVersion, refusalBody)
	fs := seedProjectConfig(t, body)

	assert.Empty(t, loadFindings(t, fs))

	got, err := afero.ReadFile(fs, paths.ConfigPath(refusalAppDir))
	require.NoError(t, err)
	assert.Equal(t, body, string(got), "the file must be byte-identical")
	_, err = fs.Stat(paths.ConfigPath(refusalAppDir) + schemaver.BackupSuffix)
	assert.ErrorIs(t, err, afero.ErrFileNotFound, "no backup without --write-upgrades")
}

// With --write-upgrades the migration is persisted: the file now declares
// schema_version, and the previous bytes are kept beside it.
func TestLoad_LegacyVersionKey_WithWriteUpgrades_PersistsTheMigration(t *testing.T) {
	writeUpgradesOn(t)
	body := fmt.Sprintf("%s: %d\n%s", configKind.LegacyKey, config.CurrentConfigVersion, refusalBody)
	fs := seedProjectConfig(t, body)
	path := paths.ConfigPath(refusalAppDir)

	assert.Empty(t, loadFindings(t, fs))

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	r, err := configKind.Upgrade(got)
	require.NoError(t, err)
	assert.Empty(t, r.Applied, "the rewritten file must already be current: %q", got)
	assert.Contains(t, string(got), versioned(config.CurrentConfigVersion, ""))
	backup, err := afero.ReadFile(fs, path+schemaver.BackupSuffix)
	require.NoError(t, err)
	assert.Equal(t, body, string(backup), "the previous bytes are kept as the backup")
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

// agentRefsGeneration is the last config generation whose agent bindings
// could store a profile ref in the fetch-address grammar; the step out of it
// re-spells them canonically.
const agentRefsGeneration = 6

const (
	fetchAddressAgentRef = "https://github.com/acme/tools@bundles/kit#profiles/dev"
	canonicalAgentRef    = "ctxloom+git://github.com/acme/tools//bundles/kit#profiles/dev"
)

// agentRefsBody is a config whose one agent binding names a bundle profile in
// the fetch-address grammar beside a bare local profile.
const agentRefsBody = "agents:\n  dev:\n    llm: claude-code\n    profiles:\n      - " + fetchAddressAgentRef + "\n      - developer\n"

// The config kind's step out of agentRefsGeneration re-spells every agent
// binding's fetch-address profile ref as its canonical ctxloom URI, needing
// nothing beyond the document, and leaves a bare local name as written.
func TestConfigKind_StepRespellsAgentProfileRefsCanonically(t *testing.T) {
	r, err := configKind.Upgrade([]byte(versioned(agentRefsGeneration, agentRefsBody)))
	require.NoError(t, err)
	assert.Equal(t, agentRefsGeneration, r.From)
	assert.Equal(t, configKind.Current(), r.To)

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(r.Data, &root))
	assert.Equal(t, []string{canonicalAgentRef, "developer"}, agentProfiles(t, root, "dev"))
}

// An old-spelling config loads migrated in memory: the binding carries the
// canonical ref, no finding is raised, and the file on disk is untouched.
func TestLoad_OldSpellingAgentRefs_MigrateInMemory(t *testing.T) {
	body := versioned(agentRefsGeneration, agentRefsBody)
	fs := seedProjectConfig(t, body)
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	mark := strictness.Checkpoint()

	cfg, err := Load(WithFS(fs), WithAppDir(refusalAppDir))
	require.NoError(t, err)
	assert.Empty(t, strictness.Since(mark), "a migratable config raises nothing")
	agent, ok := cfg.Agent("dev")
	require.True(t, ok)
	assert.Equal(t, []string{canonicalAgentRef, "developer"}, agent.Profiles)

	got, err := afero.ReadFile(fs, paths.ConfigPath(refusalAppDir))
	require.NoError(t, err)
	assert.Equal(t, body, string(got), "without --write-upgrades the file is byte-identical")
}

// --write-upgrades persists the canonical spelling at the current generation.
func TestLoad_OldSpellingAgentRefs_WithWriteUpgrades_PersistCanonical(t *testing.T) {
	writeUpgradesOn(t)
	fs := seedProjectConfig(t, versioned(agentRefsGeneration, agentRefsBody))
	path := paths.ConfigPath(refusalAppDir)

	assert.Empty(t, loadFindings(t, fs))

	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Contains(t, string(got), versioned(configKind.Current(), ""))
	assert.Contains(t, string(got), canonicalAgentRef)
	assert.NotContains(t, string(got), fetchAddressAgentRef)
}
