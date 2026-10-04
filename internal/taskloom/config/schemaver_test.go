package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// versioned is a config body declaring generation v.
func versioned(v int, rest string) string {
	return schemaver.Key + ": " + strconv.Itoa(v) + "\n" + rest
}

func configPath(dir string) string { return filepath.Join(dir, DirName, FileName) }

// writeUpgradesOn turns --write-upgrades on for the rest of the test. The
// switch is process-wide; binding a fresh flag set resets it.
func writeUpgradesOn(t *testing.T) {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	schemaver.BindWriteUpgrades(fs)
	require.NoError(t, fs.Set(schemaver.WriteUpgradesFlag, "true"))
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })
}

// TestSchemaVersion_KeylessConfigLoadsAndStaysUntouched: every taskloom
// config written before schema_version existed is generation 0 and loads as
// it always did; without --write-upgrades nothing on disk changes.
func TestSchemaVersion_KeylessConfigLoadsAndStaysUntouched(t *testing.T) {
	home := taskstest.Isolate(t)
	const homeBody, projectBody = "homing: home\n", "# project\nhoming: repo\n"
	writeConfig(t, home, homeBody)
	project := t.TempDir()
	writeConfig(t, project, projectBody)

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)

	for path, want := range map[string]string{configPath(home): homeBody, configPath(project): projectBody} {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, string(got), "a load without --write-upgrades never rewrites %s", path)
		assert.NoFileExists(t, path+schemaver.BackupSuffix)
	}
}

// TestSchemaVersion_CurrentKeyPassesMergedValidation: the merged document is
// validated against a schema with additionalProperties:false, so it must
// accept schema_version — in either layer.
func TestSchemaVersion_CurrentKeyPassesMergedValidation(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, versioned(configKind.Current(), "homing: home\n"))
	project := t.TempDir()
	writeConfig(t, project, versioned(configKind.Current(), "homing: repo\n"))

	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)
}

// TestSchemaVersion_NewerConfigIsRefusedNamingBothNumbers: a file written by a
// newer taskloom is refused — per file, so a newer HOME config is caught even
// when the project layer is current — and the error carries both numbers.
func TestSchemaVersion_NewerConfigIsRefusedNamingBothNumbers(t *testing.T) {
	newer := configKind.Current() + 1
	home := taskstest.Isolate(t)
	writeConfig(t, home, versioned(newer, "homing: home\n"))
	project := t.TempDir()
	writeConfig(t, project, versioned(configKind.Current(), "homing: repo\n"))

	_, err := Load(project, nil)
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, newer, ve.Found)
	assert.Equal(t, configKind.Current(), ve.Current)
	assert.Contains(t, err.Error(), configPath(home), "the refusal names the file")
}

// TestSchemaVersion_WriteBackOnlyWithTheFlag: --write-upgrades persists the
// in-memory migration of an older file, keeping the original as a backup;
// a file already current is left alone even with the flag.
func TestSchemaVersion_WriteBackOnlyWithTheFlag(t *testing.T) {
	taskstest.Isolate(t)
	project := t.TempDir()
	const keyless = "homing: repo\n"
	writeConfig(t, project, keyless)
	path := configPath(project)

	_, err := Load(project, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, keyless, string(got), "without the flag the file is never rewritten")

	writeUpgradesOn(t)
	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)

	got, err = os.ReadFile(path)
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, yaml.Unmarshal(got, &onDisk))
	assert.Equal(t, configKind.Current(), onDisk[schemaver.Key], "the upgrade is persisted")
	assert.Equal(t, "repo", onDisk["homing"])
	backup, err := os.ReadFile(path + schemaver.BackupSuffix)
	require.NoError(t, err)
	assert.Equal(t, keyless, string(backup), "the original is kept")

	require.NoError(t, os.Remove(path+schemaver.BackupSuffix))
	_, err = Load(project, nil)
	require.NoError(t, err)
	assert.NoFileExists(t, path+schemaver.BackupSuffix, "a current file is not written back")
}

// A config that is not YAML is its parse failure, not a version fault.
func TestSchemaVersion_MalformedConfigIsAParseFailureNotAVersionFault(t *testing.T) {
	home := taskstest.Isolate(t)
	writeConfig(t, home, "homing: [unterminated\n")

	_, err := Load(t.TempDir(), nil)
	require.Error(t, err)
	var ve *schemaver.VersionError
	assert.NotErrorAs(t, err, &ve)
}
