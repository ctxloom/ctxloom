package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestSchemaVersion_KeylessConfigIsRefused: a config that declares no
// generation is refused, in either layer, and nothing on disk changes.
func TestSchemaVersion_KeylessConfigIsRefused(t *testing.T) {
	home := taskstest.Isolate(t)
	project := t.TempDir()
	const keyless = "homing: repo\n"
	writeRawConfig(t, project, keyless)

	_, err := Load(project, nil)
	require.ErrorIs(t, err, schemaver.ErrTooOld)
	got, err := os.ReadFile(configPath(project))
	require.NoError(t, err)
	assert.Equal(t, keyless, string(got))

	writeRawConfig(t, home, keyless)
	_, err = Load(t.TempDir(), nil)
	require.ErrorIs(t, err, schemaver.ErrTooOld, "the home layer is gated too")
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

// TestSchemaVersion_WriteUpgradesLeavesACurrentFileAlone: with nothing to
// migrate, --write-upgrades writes nothing and keeps no backup.
func TestSchemaVersion_WriteUpgradesLeavesACurrentFileAlone(t *testing.T) {
	taskstest.Isolate(t)
	project := t.TempDir()
	body := versioned(configKind.Current(), "homing: repo\n")
	writeConfig(t, project, body)
	path := configPath(project)

	writeUpgradesOn(t)
	cfg, err := Load(project, nil)
	require.NoError(t, err)
	assert.Equal(t, "repo", cfg.Homing)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
	assert.NoFileExists(t, path+schemaver.BackupSuffix)
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
