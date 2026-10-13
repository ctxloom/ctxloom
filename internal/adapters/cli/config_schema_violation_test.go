package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// projectWithMistypedValue scaffolds a project and appends a key the schema
// DOES describe, with a value of the wrong type (session_reap_age is a
// duration string), returning the config file's path.
func projectWithMistypedValue(t *testing.T) string {
	t.Helper()
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	resetApp()
	t.Cleanup(resetApp)
	path := paths.ConfigPath(filepath.Join(root, paths.AppDirName))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("session_reap_age: 5\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return path
}

// A schema violation in a config file is FATAL, exactly as an unknown key is:
// a command that reads the config refuses with a typed error naming the file,
// the key path and the type the schema expected.
func TestGetConfig_SchemaViolationIsFatalByDefault(t *testing.T) {
	for _, args := range [][]string{{"agent", "list"}, {"llm", "list"}} {
		t.Run(args[0], func(t *testing.T) {
			path := projectWithMistypedValue(t)
			_, err := execRootCmd(t, args...)
			require.Error(t, err)
			require.ErrorIs(t, err, config.ErrSchemaViolation)
			var v *config.SchemaViolation
			require.True(t, errors.As(err, &v), "the refusal carries the typed violation: %v", err)
			assert.Equal(t, "session_reap_age", v.KeyPath)
			assert.Equal(t, "string", v.Expected)
			assert.True(t, errors.As(err, new(configRefusedError)), "a schema refusal is a config REFUSAL, not an unreadable config")
			assert.Contains(t, err.Error(), path)
			assert.Contains(t, err.Error(), "`session_reap_age`")
			assert.Contains(t, err.Error(), "expected string")
		})
	}
}

// Under --degraded (or CTXLOOM_DEGRADED=1) the violation is warned about and
// the command runs on the config as loaded.
func TestGetConfig_SchemaViolationOnlyWarnsWhenDegraded(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		projectWithMistypedValue(t)
		_, err := execRootCmd(t, "agent", "list", "--degraded")
		require.NoError(t, err)
	})
	t.Run("env", func(t *testing.T) {
		projectWithMistypedValue(t)
		t.Setenv("CTXLOOM_DEGRADED", "1")
		_, err := execRootCmd(t, "agent", "list")
		require.NoError(t, err)
	})
}
