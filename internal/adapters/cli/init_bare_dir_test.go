package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRunInit_BareCtxloomDir_ScaffoldsLikeAFreshProject: a .ctxloom with no
// config.yaml is not a project — something abandoned or partial left it (a
// marker, a state dir). Treating it as "already exists" skipped the scaffold
// and left a directory every command treats as a project with no config,
// with init itself no longer able to repair it. A project is told apart by
// its config file.
func TestRunInit_BareCtxloomDir_ScaffoldsLikeAFreshProject(t *testing.T) {
	testsupport.Isolate(t)
	project := t.TempDir()
	appDir := filepath.Join(project, paths.AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, paths.ProjectIDFileName), []byte("left-behind\n"), 0o644))
	chdir(t, project)
	resetApp()
	t.Cleanup(resetApp)
	withInitFlags(t, true)
	origClone := cloneConfiguredRemotesFn
	cloneConfiguredRemotesFn = func(*cobra.Command, string) {}
	t.Cleanup(func() { cloneConfiguredRemotesFn = origClone })

	out := captureStdout(t, func() {
		require.NoError(t, runInit(initTestCmd(), nil))
	})

	assert.FileExists(t, paths.ConfigPath(appDir), "a bare .ctxloom is scaffolded")
	assert.NotContains(t, out, fmt.Sprintf(initAlreadyExistsFormat, appDir), "a bare .ctxloom is not an existing project")
}
