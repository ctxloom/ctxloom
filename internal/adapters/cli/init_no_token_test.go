package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRunInit_NonInteractive_NoToken_SucceedsWithAWarning: a non-interactive
// init with no agent token exported has set the project up; only the launch
// of the setup interview was skipped. That is a success with a warning, not a
// failure a script reads as "init did not work".
func TestRunInit_NonInteractive_NoToken_SucceedsWithAWarning(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(claude.OAuthTokenEnv, "")
	project := t.TempDir()
	chdir(t, project)
	resetApp()
	t.Cleanup(resetApp)
	withInitFlags(t, true)
	initSkipLaunch = false
	origClone := cloneConfiguredRemotesFn
	cloneConfiguredRemotesFn = func(*cobra.Command, string) {}
	t.Cleanup(func() { cloneConfiguredRemotesFn = origClone })

	var warned bytes.Buffer
	t.Cleanup(clidiag.SetSink(&warned))
	var err error
	captureStdout(t, func() { err = runInit(initTestCmd(), nil) })

	require.NoError(t, err, "setup succeeded; only the launch was skipped")
	assert.FileExists(t, filepath.Join(project, ".ctxloom", "config.yaml"))
	lead := strings.SplitN(initLaunchSkippedNoTokenFormat, "%v", 2)[0]
	assert.Contains(t, warned.String(), lead, "the skipped launch is warned, not silent")
	assert.Contains(t, warned.String(), claude.OAuthTokenEnv, "with the token's fix")
}
