package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stubProbe swaps the engine-version probe for the duration of one test, so no
// engine binary has to be installed for either outcome to be exercised.
func stubProbe(t *testing.T, version string, err error) {
	t.Helper()
	orig := probeEngineVersion
	probeEngineVersion = func(context.Context, string) (string, error) { return version, err }
	t.Cleanup(func() { probeEngineVersion = orig })
}

// Session start is the ONLY moment at which "what engine is installed" and
// "what engine will write this session's transcript" are the same fact. If the
// version is not captured here it cannot be recovered later: probing at read
// time answers for whatever is installed THEN, which for an upgraded engine is
// the wrong format entirely.
func TestAssignSession_RecordsTheProbedEngineVersion(t *testing.T) {
	testsupport.Isolate(t)
	stubProbe(t, "2.1.225", nil)

	entry, err := AssignSession(t.TempDir(), "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "2.1.225", entry.EngineVersion, "the returned entry must carry what was probed")

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "2.1.225", stored.EngineVersion,
		"the version must be in the INDEX, not just the returned value — a later process is what reads it")
}

// A version AHEAD of .github/engine-versions.env's pin (claude installed at
// 2.1.225 against a 2.1.214 pin on this host) is recorded as-is. Recording is
// not validation: what was actually running gets written down, and whether an
// adapter covers it is the read path's decision, made once, in one place.
func TestAssignSession_RecordsAVersionAheadOfThePin(t *testing.T) {
	testsupport.Isolate(t)
	stubProbe(t, "2.1.225", nil)

	entry, err := AssignSession(t.TempDir(), "claude-code")
	require.NoError(t, err)
	assert.Equal(t, "2.1.225", entry.EngineVersion,
		"an installed version ahead of the tested-version lock is still the truth about what ran")
}

// And a version BEHIND the pin (codex installed at 0.144.4 against a 0.144.6
// pin on this host) is recorded just as plainly — the pin is not a floor on
// what a user may have installed.
func TestAssignSession_RecordsAVersionBehindThePin(t *testing.T) {
	testsupport.Isolate(t)
	stubProbe(t, "0.144.4", nil)

	entry, err := AssignSession(t.TempDir(), "codex")
	require.NoError(t, err)
	assert.Equal(t, "0.144.4", entry.EngineVersion)
}

// A FAILED probe must not fail the run. Refusing to start a session because an
// engine could not be asked its version would deny a user their working engine
// over a diagnostic; the refusal that actually protects something happens on
// the READ path, where an unrecorded version is treated as unknown and stops.
func TestAssignSession_ProbeFailureLeavesTheVersionUnsetWithoutFailingTheRun(t *testing.T) {
	testsupport.Isolate(t)
	stubProbe(t, "", &engineversion.BinaryAbsentError{Engine: "kiro", Err: errors.New("not on PATH")})

	entry, err := AssignSession(t.TempDir(), "kiro")
	require.NoError(t, err, "an unprobeable engine must still get a harp and still run")
	assert.NotEmpty(t, entry.HarpName)
	assert.Empty(t, entry.EngineVersion, "a failed probe must record NOTHING, not a guess and not an empty-looking success")

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Empty(t, stored.EngineVersion,
		"the index must carry no version, which is exactly what the read path reads as unknown")
}
