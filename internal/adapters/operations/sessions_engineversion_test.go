package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stubProbe swaps the engine-version probe for the duration of one test, so no
// engine binary has to be installed for either outcome to be exercised.
func stubProbe(t *testing.T, version string, err error) {
	t.Helper()
	orig := probeEngineVersion
	probeEngineVersion = func(*App, context.Context, string, string) (string, error) { return version, err }
	t.Cleanup(func() { probeEngineVersion = orig })
}

// Session start is the ONLY moment at which "what engine is installed" and
// "what engine will write this session's transcript" are the same fact, and
// what was actually running is recorded as-is, whether it is ahead of or
// behind .github/engine-versions.env's pin. Recording is not validation:
// whether an adapter covers the version is the read path's decision, made
// once, in one place.
func TestRecordSessionEngineVersion_RecordsWhatWasProbedWhateverThePin(t *testing.T) {
	for _, version := range []string{"2.1.225", "0.144.4"} {
		t.Run(version, func(t *testing.T) {
			testsupport.Isolate(t)
			entry, err := AssignSessionHarp(t.TempDir(), "claude-code", t.TempDir())
			require.NoError(t, err)
			stubProbe(t, version, nil)
			got, ok := new(App).RecordSessionEngineVersion(context.Background(), entry.HarpName, "claude-code", "")
			require.True(t, ok)
			assert.Equal(t, version, got)
			stored, err := GetSession(entry.HarpName)
			require.NoError(t, err)
			require.NotNil(t, stored)
			assert.Equal(t, version, stored.EngineVersion, "the INDEX carries it: a later process is what reads it")
		})
	}
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
	probeEngineVersion = func(*App, context.Context, string, string) (string, error) {
		probed++
		return "2.1.225", nil
	}
	t.Cleanup(func() { probeEngineVersion = orig })

	entry, err := AssignSessionHarp(t.TempDir(), "claude-code", t.TempDir())
	require.NoError(t, err)
	assert.NotEmpty(t, entry.HarpName, "the address is minted")
	assert.Zero(t, probed, "and nobody's CLI was executed to get it")
	assert.Empty(t, entry.EngineVersion, "so there is no version yet — recording it is the other half's job")
}

// The other half, called on its own, still records against an already-minted
// harp — the shape the spawn path uses once the run is registered.
func TestRecordSessionEngineVersion_RecordsAgainstAnAlreadyMintedHarp(t *testing.T) {
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code", t.TempDir())
	require.NoError(t, err)
	stubProbe(t, "2.1.225", nil)

	version, ok := new(App).RecordSessionEngineVersion(context.Background(), entry.HarpName, "claude-code", "")
	require.True(t, ok, "a successful probe records")
	assert.Equal(t, "2.1.225", version)

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "2.1.225", stored.EngineVersion,
		"the split must not lose the record: the index is what a later process reads")
}

// The version recorded is the one the session's launch spawns: the probe
// asks the binary the caller names (the launch's label binary, a label's
// binary_path), not the engine's default install — so a session on a pinned
// binary is read back with the reader for the version that wrote it.
func TestRecordSessionEngineVersion_ProbesTheBinaryTheSessionLaunched(t *testing.T) {
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code", t.TempDir())
	require.NoError(t, err)
	var asked []string
	orig := probeEngineVersion
	probeEngineVersion = func(_ *App, _ context.Context, name, binary string) (string, error) {
		asked = append(asked, name+" "+binary)
		return "2.1.300", nil
	}
	t.Cleanup(func() { probeEngineVersion = orig })

	version, ok := new(App).RecordSessionEngineVersion(context.Background(), entry.HarpName, "claude-code", "/opt/claude-next/bin/claude")
	require.True(t, ok)
	assert.Equal(t, "2.1.300", version)
	assert.Equal(t, []string{"claude-code /opt/claude-next/bin/claude"}, asked,
		"the probe asks the binary the session launched, not the engine's default")
}

// A probe failure still costs the session nothing — the refusal lands on the
// read path, not here.
func TestRecordSessionEngineVersion_AFailedProbeRecordsNothingAndReportsIt(t *testing.T) {
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code", t.TempDir())
	require.NoError(t, err)
	stubProbe(t, "", errors.New("boom"))

	version, ok := new(App).RecordSessionEngineVersion(context.Background(), entry.HarpName, "claude-code", "")
	assert.False(t, ok, "a failed probe records nothing and says so")
	assert.Empty(t, version)

	stored, err := GetSession(entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Empty(t, stored.EngineVersion, "an unrecorded version is what the read path refuses on")
}

// The caller's context reaches the probe, on both session-start paths: a
// child's record (RecordSessionEngineVersion) and a top-level run's
// (App.LaunchDeps' ProbeVersion, which StartRun calls). It used to be
// discarded for context.Background() outright, which is why a wedged version
// command had no deadline at all however careful the caller was.
func TestEngineVersionProbe_ThreadsTheCallersContext(t *testing.T) {
	testsupport.Isolate(t)
	var got []context.Context
	orig := probeEngineVersion
	probeEngineVersion = func(_ *App, ctx context.Context, _, _ string) (string, error) {
		got = append(got, ctx)
		return "", errors.New("no")
	}
	t.Cleanup(func() { probeEngineVersion = orig })

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code", t.TempDir())
	require.NoError(t, err)
	_, ok := new(App).RecordSessionEngineVersion(ctx, entry.HarpName, "claude-code", "")
	require.False(t, ok, "a failed probe records nothing")

	deps, err := testApp(t).LaunchDeps(context.Background())
	require.NoError(t, err)
	require.NotNil(t, deps.ProbeVersion, "a real launch's deps carry the probe, or no top-level session records a version")
	_, _ = deps.ProbeVersion(ctx, "claude-code", "")

	require.Len(t, got, 2)
	for _, c := range got {
		assert.Equal(t, "caller", c.Value(ctxKey{}),
			"the probe ran under the CALLER's context, not a fresh Background one")
	}
}
