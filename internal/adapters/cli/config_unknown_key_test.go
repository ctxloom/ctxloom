package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// projectWithUnknownKey scaffolds a project and appends a key the config
// schema does not describe, returning the project root.
func projectWithUnknownKey(t *testing.T) string {
	t.Helper()
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	resetApp()
	t.Cleanup(resetApp)
	path := paths.ConfigPath(filepath.Join(root, paths.AppDirName))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("frobnicate: 1\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return root
}

// An unknown config key is a FATAL finding: a command that reads the config
// refuses, naming the key, rather than running on a setting nobody chose.
func TestGetConfig_UnknownKeyIsFatalByDefault(t *testing.T) {
	// llm list degrades to the built-in backends over an UNREADABLE config;
	// a config it refused is not one.
	for _, args := range [][]string{{"agent", "list"}, {"llm", "list"}} {
		t.Run(args[0], func(t *testing.T) {
			projectWithUnknownKey(t)
			_, err := execRootCmd(t, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "`frobnicate`")
		})
	}
}

// Under --degraded (or CTXLOOM_DEGRADED=1) the config loads best-effort: the
// unknown key is warned about and the command runs.
func TestGetConfig_UnknownKeyOnlyWarnsWhenDegraded(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		projectWithUnknownKey(t)
		_, err := execRootCmd(t, "agent", "list", "--degraded")
		require.NoError(t, err)
	})
	t.Run("env", func(t *testing.T) {
		projectWithUnknownKey(t)
		t.Setenv("CTXLOOM_DEGRADED", "1")
		_, err := execRootCmd(t, "agent", "list")
		require.NoError(t, err)
	})
}
