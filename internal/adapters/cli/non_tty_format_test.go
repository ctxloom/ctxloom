package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

// withNonTTYFormat sets non_tty_format through its env layer for one test
// and undoes the process-wide state the root's pre-run installs from it.
func withNonTTYFormat(t *testing.T, value string) {
	t.Helper()
	testsupport.Isolate(t)
	chdir(t, t.TempDir())
	t.Setenv("CTXLOOM_CONFIG_NON_TTY_FORMAT", value)
	resetApp()
	t.Cleanup(resetApp)
	t.Cleanup(cobrafmt.SetOffTerminalDefault(func() clifmt.Format { return "" }))
	t.Cleanup(func() { clidiag.SetStructured(false) })
}

// `version` reads no config value of its own, so non_tty_format reaches it
// only because rendering opens the configuration before deriving a format
// (openForDerivedFormat). Without that it derived json whatever the key said.
func TestNonTTYFormat_ACommandThatReadsNoConfigStillFollowsIt(t *testing.T) {
	withNonTTYFormat(t, "text")

	out, err := execRootCmd(t, "version")
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(strings.TrimSpace(out), "{"),
		"non_tty_format: text must render version as text off a terminal, got %q", out)
}

// An explicit --format is a request, and a request beats the configured
// off-terminal default.
func TestNonTTYFormat_AnExplicitFormatWins(t *testing.T) {
	withNonTTYFormat(t, "text")

	out, err := execRootCmd(t, "version", "--format", "json")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(out), "{"), "explicit --format json, got %q", out)
}

// `init` pins its .ctxloom directory before anything opens the process's one
// configuration (pinAppDir). Reading non_tty_format must not open it early:
// the root's pre-run installs a source, never a value. Driven through the
// real command tree, because a direct runInit call skips the pre-run and
// would pass either way.
func TestNonTTYFormat_InitStillPinsItsDirectory(t *testing.T) {
	withNonTTYFormat(t, "text")
	withInitFlags(t, true)
	origClone := cloneConfiguredRemotesFn
	cloneConfiguredRemotesFn = func(*cobra.Command, string) {}
	t.Cleanup(func() { cloneConfiguredRemotesFn = origClone })

	_, err := execRootCmd(t, "init")
	require.NoError(t, err, "init through the real command tree must not find the configuration already open")
}
