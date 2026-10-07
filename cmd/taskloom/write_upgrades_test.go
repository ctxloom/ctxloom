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

// TestWriteUpgrades_ARefusedConfigIsNeverRewritten drives the real root: a
// config that declares no generation is refused, and --write-upgrades, a
// persistent flag, persists migrations only — a refusal migrated nothing.
func TestWriteUpgrades_ARefusedConfigIsNeverRewritten(t *testing.T) {
	for _, flags := range [][]string{nil, {"--" + schemaver.WriteUpgradesFlag}} {
		path, body := keylessProjectConfig(t)

		out, err := executeTaskloom(t, append([]string{"list", "--format", "text"}, flags...)...)
		require.ErrorIs(t, err, schemaver.ErrTooOld, out)

		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
		assert.NoFileExists(t, path+schemaver.BackupSuffix)
	}
}
