package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	taskloomconfig "github.com/ctxloom/ctxloom/internal/taskloom/config"
)

// keylessProjectConfig writes a generation-0 (keyless) project config into a
// fresh isolated project and returns its path and body.
func keylessProjectConfig(t *testing.T) (path, body string) {
	t.Helper()
	project := taskstest.ProjectDir(t)
	path = filepath.Join(project, taskloomconfig.DirName, taskloomconfig.FileName)
	body = "homing: home\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path, body
}

// TestWriteUpgrades_FlagPersistsTheConfigMigration drives the real root:
// --write-upgrades is a persistent flag, and with it an older config is
// rewritten at the current generation, the original kept as a backup.
func TestWriteUpgrades_FlagPersistsTheConfigMigration(t *testing.T) {
	path, body := keylessProjectConfig(t)

	out, err := executeTaskloom(t, "list", "--format", "text", "--"+schemaver.WriteUpgradesFlag)
	require.NoError(t, err, out)

	backup, err := os.ReadFile(path + schemaver.BackupSuffix)
	require.NoError(t, err)
	assert.Equal(t, body, string(backup))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotEqual(t, body, string(got), "the migration is written back")
}

// TestWriteUpgrades_WithoutTheFlagTheConfigIsUntouched is the other half:
// the same load without the flag migrates in memory only.
func TestWriteUpgrades_WithoutTheFlagTheConfigIsUntouched(t *testing.T) {
	path, body := keylessProjectConfig(t)

	out, err := executeTaskloom(t, "list", "--format", "text")
	require.NoError(t, err, out)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
	assert.NoFileExists(t, path+schemaver.BackupSuffix)
}
