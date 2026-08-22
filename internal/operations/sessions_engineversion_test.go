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

	entry, err := AssignSession(context.Background(), t.TempDir(), "claude-code")
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

	entry, err := AssignSession(context.Background(), t.TempDir(), "claude-code")
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

	entry, err := AssignSession(context.Background(), t.TempDir(), "codex")
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

	entry, err := AssignSession(context.Background(), t.TempDir(), "kiro")
	require.NoError(t, err, "an unprobeable engine must still get a harp and still run")
	assert.NotEmpty(t, entry.HarpName)
	assert.Empty(t, entry.EngineVersion, "a failed probe must record NOTHING, not a guess and not an empty-looking success")

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Empty(t, stored.EngineVersion,
		"the index must carry no version, which is exactly what the read path reads as unknown")
}

// AssignSessionHarp is the half that exists so a caller whose next act is to
// PUBLISH the harp can do that first. Its whole value is that it does not
// exec anything: the probe used to sit between minting a delegated child's
// address and registering that child anywhere visible, and a `--version` that
// hung there left a spawn invisible for minutes while its caller concluded
// nothing had been created (task affected-yearly). A probe creeping back into
// this half restores exactly that.
func TestAssignSessionHarp_MintsTheAddressWithoutProbingTheEngine(t *testing.T) {
	testsupport.Isolate(t)
	probed := 0
	orig := probeEngineVersion
	probeEngineVersion = func(context.Context, string) (string, error) {
		probed++
		return "2.1.225", nil
	}
	t.Cleanup(func() { probeEngineVersion = orig })

	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	assert.NotEmpty(t, entry.HarpName, "the address is minted")
	assert.Zero(t, probed, "and nobody's CLI was executed to get it")
	assert.Empty(t, entry.EngineVersion, "so there is no version yet — recording it is the other half's job")
}

// The other half, called on its own, still records against an already-minted
// harp — the shape the spawn path uses once the run is registered.
func TestRecordSessionEngineVersion_RecordsAgainstAnAlreadyMintedHarp(t *testing.T) {
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	stubProbe(t, "2.1.225", nil)

	version, ok := RecordSessionEngineVersion(context.Background(), entry.HarpName, "claude-code")
	require.True(t, ok, "a successful probe records")
	assert.Equal(t, "2.1.225", version)

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "2.1.225", stored.EngineVersion,
		"the split must not lose the record: the index is what a later process reads")
}

// A probe failure still costs the session nothing — the refusal lands on the
// read path, not here.
func TestRecordSessionEngineVersion_AFailedProbeRecordsNothingAndReportsIt(t *testing.T) {
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	stubProbe(t, "", errors.New("boom"))

	version, ok := RecordSessionEngineVersion(context.Background(), entry.HarpName, "claude-code")
	assert.False(t, ok, "a failed probe records nothing and says so")
	assert.Empty(t, version)

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Empty(t, stored.EngineVersion, "an unrecorded version is what the read path refuses on")
}

// The caller's context reaches the probe. It used to be discarded for
// context.Background() outright, which is why a wedged version command had no
// deadline at all on this path however careful the caller was.
func TestAssignSession_ThreadsTheCallersContextIntoTheProbe(t *testing.T) {
	testsupport.Isolate(t)
	var got context.Context
	orig := probeEngineVersion
	probeEngineVersion = func(ctx context.Context, _ string) (string, error) {
		got = ctx
		return "", errors.New("no")
	}
	t.Cleanup(func() { probeEngineVersion = orig })

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	_, err := AssignSession(ctx, t.TempDir(), "claude-code")
	require.NoError(t, err, "a failed probe never fails the session")
	require.NotNil(t, got)
	assert.Equal(t, "caller", got.Value(ctxKey{}),
		"the probe ran under the CALLER's context, not a fresh Background one")
}
