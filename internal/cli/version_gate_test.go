package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/version"
)

// unstampedBuild puts this process into the state a binary built without the
// task runner's ldflags is in, and restores the test binary's own stamp after.
//
// "dev" rather than "" on purpose: it is the sentinel this gate exists to have
// retired, and being non-empty it makes the ASSERTIONS discriminating — the
// version command echoes it, the abort header does not, so "did the command
// actually run?" is answerable from the captured output rather than inferred
// from an exit code alone.
func unstampedBuild(t *testing.T) {
	t.Helper()
	require.False(t, version.ValidStamp("dev"), "the retired sentinel must not be a usable stamp, or this test proves nothing")
	orig := version.Version
	version.Version = "dev"
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		version.Version = orig
		strictness.Reset()
		strictness.SetDegraded(false)
		if f := rootCmd.PersistentFlags().Lookup("degraded"); f != nil {
			require.NoError(t, f.Value.Set(f.DefValue))
			f.Changed = false
		}
	})
}

// TestUnstampedBuild_RefusesByDefault is the strict arm. A binary that cannot
// name its own build must not do the work: the run exits with the fatal-findings
// status and the command's own output never appears.
//
// The subject is `version`, deliberately — the command with the most obvious
// claim to run anyway. If even it refuses, the gate is on the binary rather
// than on a subset of launch paths.
func TestUnstampedBuild_RefusesByDefault(t *testing.T) {
	unstampedBuild(t)

	out, err := runRoot(t, "version")

	require.Error(t, err, "an unstamped binary must refuse")
	var exitErr *ExitError
	require.True(t, errors.As(err, &exitErr), "the refusal must carry an exit status, not just fail: got %#v", err)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code, "a startup refusal reports the fatal-findings status")
	assert.NotContains(t, out, "dev", "the command RAN and reported the unusable stamp; refusing must stop it before dispatch")
	assert.Contains(t, out, versionStampFixIt, "the refusal must state the remedy, not just the complaint")
}

// TestUnstampedBuild_LaunchesUnderDegraded is the other arm, and it is not
// optional: --degraded is a promise that the user always reaches a working
// tool, and a test covering only the refusal would let this half rot into a
// second refusal with nothing going red.
func TestUnstampedBuild_LaunchesUnderDegraded(t *testing.T) {
	unstampedBuild(t)

	out, err := runRoot(t, "--degraded", "version")

	require.NoError(t, err, "--degraded must warn and continue, not refuse")
	assert.Contains(t, out, "dev", "the command must actually have RUN and produced its output, not merely exited 0")
	assert.NotContains(t, out, "aborting", "nothing may abort under --degraded for a degradable finding")
}

// TestStampedBuild_Runs pins the direction that a gate refusing everything
// would also satisfy. It doubles as the proof that the test harness's own
// stamp (testsupport.StampTestBinary, installed by SandboxedMain) is what
// carries every other test in this package past the gate.
func TestStampedBuild_Runs(t *testing.T) {
	require.True(t, version.ValidStamp(version.Version),
		"this test binary must arrive already stamped by SandboxedMain; got %q", version.Version)

	out, err := runRoot(t, "version")

	require.NoError(t, err)
	assert.Contains(t, strings.TrimSpace(out), version.Version, "a stamped binary runs and reports its stamp")
}
